package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"iter"
	"math"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"cloud.google.com/go/auth/credentials"
	"cloud.google.com/go/auth/httptransport"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/ec2/imds"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/document"
	bt "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"google.golang.org/genai"
)

// Embed in Source and sourceInput. No credential, filesystem path, or token lives
// in this DTO; service-account material uses the existing account CredentialRef.
type CloudProviderConfig struct {
	CloudConfig *CloudSelection `json:"cloud_config,omitempty"`
}
type CloudSelection struct {
	AWSRegion             string `json:"aws_region,omitempty"`
	AWSProfile            string `json:"aws_profile,omitempty"`
	VertexProject         string `json:"vertex_project,omitempty"`
	VertexLocation        string `json:"vertex_location,omitempty"`
	VertexCredentialsMode string `json:"vertex_credentials_mode,omitempty"`
}

func cloudSelection(src Source) *CloudSelection {
	// JSON extraction keeps this isolated module compilable before root embeds its
	// DTO in Source. Once embedded, this reads the exact persisted source snapshot.
	var v CloudProviderConfig
	raw, e := json.Marshal(src)
	if e == nil {
		_ = json.Unmarshal(raw, &v)
	}
	return v.CloudConfig
}
func cloudProvider(src Source) bool { return src.Provider == "bedrock" || src.Provider == "vertex" }
func cloudAuthType(provider string, c *CloudSelection) string {
	if c == nil {
		return ""
	}
	if provider == "bedrock" {
		return "aws_profile"
	}
	if provider == "vertex" {
		if c.VertexCredentialsMode == "adc" {
			return "google_adc"
		}
		if c.VertexCredentialsMode == "service_account" {
			return "service_account"
		}
	}
	return ""
}
func cloudLabel(v string) bool {
	if v == "" || len(v) > 128 || strings.TrimSpace(v) != v {
		return false
	}
	return !strings.ContainsAny(v, "/\\\r\n\x00")
}
func cloudDNSPart(v string) bool {
	if !cloudLabel(v) || strings.HasPrefix(v, "-") || strings.HasSuffix(v, "-") {
		return false
	}
	for _, r := range v {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}
func validateCloudSelection(provider string, c *CloudSelection) error {
	bad := func() error {
		return &accountingError{Status: 422, Field: "cloud_config", Message: "需明确选择完整云 provider 配置及凭据方式"}
	}
	if c == nil {
		return bad()
	}
	switch provider {
	case "bedrock":
		if !cloudDNSPart(c.AWSRegion) || !cloudLabel(c.AWSProfile) || c.VertexProject != "" || c.VertexLocation != "" || c.VertexCredentialsMode != "" {
			return bad()
		}
	case "vertex":
		if !cloudDNSPart(c.VertexProject) || !cloudDNSPart(c.VertexLocation) || c.AWSRegion != "" || c.AWSProfile != "" || (c.VertexCredentialsMode != "adc" && c.VertexCredentialsMode != "service_account") {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
func cloudConfigured(src Source) bool {
	c := cloudSelection(src)
	if validateCloudSelection(src.Provider, c) != nil {
		return false
	}
	return c.VertexCredentialsMode != "service_account" || src.CredentialRef != ""
}
func cloudEndpoint(provider string, c *CloudSelection) string {
	if validateCloudSelection(provider, c) != nil {
		return ""
	}
	if provider == "bedrock" {
		endpoint, e := bedrockruntime.NewDefaultEndpointResolverV2().ResolveEndpoint(context.Background(), bedrockruntime.EndpointParameters{Region: aws.String(c.AWSRegion), UseFIPS: aws.Bool(false), UseDualStack: aws.Bool(false)})
		if e != nil {
			return ""
		}
		return endpoint.URI.String()
	}
	if c.VertexLocation == "global" {
		return "https://aiplatform.googleapis.com"
	}
	return "https://" + c.VertexLocation + "-aiplatform.googleapis.com"
}
func validateCloudRequest(src Source, body map[string]json.RawMessage, protocol string) error {
	_, _, e := cloudPrepare(src.Provider, cloudSelection(src), body, protocol)
	return e
}

type cloudBedrockAPI interface {
	Converse(context.Context, *bedrockruntime.ConverseInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseOutput, error)
	ConverseStream(context.Context, *bedrockruntime.ConverseStreamInput, ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamOutput, error)
}
type cloudVertexAPI interface {
	GenerateContent(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error)
	GenerateContentStream(context.Context, string, []*genai.Content, *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error]
}
type cloudSDKFactories struct {
	Bedrock func(context.Context, *CloudSelection, *http.Client) (cloudBedrockAPI, error)
	Vertex  func(context.Context, *CloudSelection, *http.Client, Secrets, string) (cloudVertexAPI, error)
}

func cloudFactories() cloudSDKFactories {
	return cloudSDKFactories{
		Bedrock: func(ctx context.Context, c *CloudSelection, h *http.Client) (cloudBedrockAPI, error) {
			cfg, e := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(c.AWSRegion), awsconfig.WithSharedConfigProfile(c.AWSProfile), awsconfig.WithHTTPClient(h), awsconfig.WithEC2IMDSClientEnableState(imds.ClientDisabled), awsconfig.WithRetryer(func() aws.Retryer { return aws.NopRetryer{} }))
			if e != nil {
				return nil, errors.New("选定 AWS profile 不可用")
			}
			// Explicit endpoint defeats environment/profile endpoint overrides; AWS SDK
			// still performs endpoint-aware SigV4 and signs the selected region.
			cfg.BaseEndpoint = aws.String(cloudEndpoint("bedrock", c))
			return bedrockruntime.NewFromConfig(cfg, func(o *bedrockruntime.Options) { o.Retryer = aws.NopRetryer{}; o.RetryMaxAttempts = 1 }), nil
		},
		Vertex: func(ctx context.Context, c *CloudSelection, h *http.Client, v Secrets, ref string) (cloudVertexAPI, error) {
			opts := &credentials.DetectOptions{Scopes: []string{"https://www.googleapis.com/auth/cloud-platform"}, Client: h, DisableAsyncRefresh: true}
			if c.VertexCredentialsMode == "service_account" {
				raw, e := v.Get(ref)
				if e != nil {
					return nil, errors.New("选定服务账号凭据不可读")
				}
				if e = validateCloudServiceAccount([]byte(raw)); e != nil {
					return nil, e
				}
				opts.CredentialsJSON = []byte(raw)
			}
			creds, e := credentials.DetectDefault(opts)
			if e != nil {
				return nil, errors.New("选定 Google 凭据不可用")
			}
			if e = httptransport.AddAuthorizationMiddleware(h, creds); e != nil {
				return nil, errors.New("Google 凭据 transport 初始化失败")
			}
			client, e := genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: c.VertexProject, Location: c.VertexLocation, Credentials: creds, HTTPClient: h, HTTPOptions: genai.HTTPOptions{APIVersion: "v1", BaseURL: cloudEndpoint("vertex", c), RetryOptions: &genai.HTTPRetryOptions{Attempts: genai.Ptr(int32(1))}}})
			if e != nil {
				return nil, errors.New("Vertex SDK 初始化失败")
			}
			return client.Models, nil
		},
	}
}
func validateCloudServiceAccount(raw []byte) error {
	var v struct {
		Type     string `json:"type"`
		TokenURI string `json:"token_uri"`
		Universe string `json:"universe_domain"`
		Email    string `json:"client_email"`
		Key      string `json:"private_key"`
	}
	if len(raw) > 1<<20 || json.Unmarshal(raw, &v) != nil || v.Type != "service_account" || v.Email == "" || v.Key == "" || (v.TokenURI != "" && v.TokenURI != "https://oauth2.googleapis.com/token") || (v.Universe != "" && v.Universe != "googleapis.com") {
		return errors.New("只支持官方 Google 服务账号 JSON 凭据")
	}
	return nil
}

type cloudWireTap struct {
	mu          sync.Mutex
	usage       Usage
	frames      []Usage
	trackFrames bool
	err         error
	sent        atomic.Bool
}

func (t *cloudWireTap) observe(raw []byte) {
	var v struct {
		Usage map[string]json.RawMessage `json:"usageMetadata"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	frame := Usage{}
	for _, p := range []struct {
		k  string
		to **int64
		at **int64
	}{{"promptTokenCount", &t.usage.Input, &frame.Input}, {"candidatesTokenCount", &t.usage.Output, &frame.Output}, {"cachedContentTokenCount", &t.usage.Cached, &frame.Cached}, {"thoughtsTokenCount", &t.usage.Reasoning, &frame.Reasoning}} {
		if value, present := v.Usage[p.k]; present {
			var n int64
			if json.Unmarshal(value, &n) != nil || n < 0 {
				t.err = errors.New("Vertex usage 无效")
				continue
			}
			if *p.to != nil && n < **p.to {
				t.err = errors.New("Vertex 累计 usage 回退")
				continue
			}
			*p.to = &n
			*p.at = &n
		}
	}
	if t.trackFrames {
		// Match wire observations to SDK yields rather than using cumulative
		// values from later events that the HTTP scanner may have prefetched.
		if len(t.frames) >= 8192 {
			t.err = errors.New("Vertex 待处理事件超过限制")
			return
		}
		t.frames = append(t.frames, frame)
	}
}
func (t *cloudWireTap) frame() (Usage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.err != nil {
		return Usage{}, t.err
	}
	if len(t.frames) == 0 {
		return Usage{}, errors.New("Vertex SDK 响应缺少对应原生事件")
	}
	u := t.frames[0]
	t.frames[0] = Usage{}
	t.frames = t.frames[1:]
	return u, nil
}
func (t *cloudWireTap) current() (Usage, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	u := t.usage
	if u.Output != nil && u.Reasoning != nil {
		n := *u.Output + *u.Reasoning
		if n < 0 {
			return Usage{}, errors.New("Vertex usage 溢出")
		}
		u.Output = &n
	}
	return u, t.err
}

type cloudTransport struct {
	a   *App
	src Source
	max int64
	tap *cloudWireTap
}

func (t cloudTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.GetBody = nil
	generation := req.Method == "POST" && (strings.Contains(req.URL.Path, "/converse") || strings.Contains(req.URL.Path, ":generateContent") || strings.Contains(req.URL.Path, ":streamGenerateContent"))
	if generation {
		t.tap.sent.Store(true)
	}
	response, e := t.a.doUpstream(req, t.src)
	if e != nil {
		return nil, e
	}
	if response.Body != nil {
		b := &cloudLimitedBody{ReadCloser: response.Body, left: t.max}
		if generation && t.src.Provider == "vertex" {
			b.tap = t.tap
			b.stream = strings.Contains(response.Header.Get("Content-Type"), "text/event-stream")
		}
		response.Body = b
	}
	return response, nil
}

type cloudLimitedBody struct {
	io.ReadCloser
	left   int64
	tap    *cloudWireTap
	stream bool
	buffer []byte
	done   bool
}

func (b *cloudLimitedBody) Read(p []byte) (int, error) {
	if b.left < 0 {
		return 0, errors.New("云响应超过限制")
	}
	if int64(len(p)) > b.left+1 {
		p = p[:b.left+1]
	}
	n, e := b.ReadCloser.Read(p)
	b.left -= int64(n)
	if b.left < 0 {
		return 0, errors.New("云响应超过限制")
	}
	if b.tap != nil {
		b.buffer = append(b.buffer, p[:n]...)
		if b.stream {
			for {
				index := bytes.IndexByte(b.buffer, '\n')
				if index < 0 {
					break
				}
				line := b.buffer[:index]
				if bytes.HasPrefix(line, []byte("data:")) {
					b.tap.observe(bytes.TrimSpace(line[5:]))
				}
				b.buffer = bytes.Clone(b.buffer[index+1:])
			}
			if len(b.buffer) > 1<<20 {
				return n, errors.New("Vertex SSE event 超过限制")
			}
		} else if e == io.EOF && !b.done {
			b.tap.observe(b.buffer)
			b.done = true
			b.buffer = nil
		}
	}
	return n, e
}
func (a *App) cloudHTTPClient(src Source, max int64, tap *cloudWireTap) *http.Client {
	return &http.Client{Transport: cloudTransport{a, src, max, tap}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// Called only inside the ordinary admitted executor. The returned stream body
// is lazy: reading it advances the SDK iterator in the existing request task.
// There is no second executor or detached generation goroutine.
func (a *App) cloudUpstream(ctx context.Context, src Source, body map[string]json.RawMessage, protocol string, max int64) (*http.Response, error) {
	selection := cloudSelection(src)
	if a.Config.PublicAPIBase != "" && (src.Provider == "bedrock" || selection == nil || selection.VertexCredentialsMode != "service_account") {
		return nil, &cloudBeforeSendError{Message: "企业来源不能使用主机 AWS profile 或 Google ADC；需要本租户显式凭据"}
	}
	return a.cloudUpstreamWith(ctx, src, cloudSelection(src), body, protocol, max, cloudFactories())
}
func (a *App) cloudUpstreamWith(ctx context.Context, src Source, c *CloudSelection, body map[string]json.RawMessage, protocol string, max int64, f cloudSDKFactories) (*http.Response, error) {
	bedrock, vertex, e := cloudPrepare(src.Provider, c, body, protocol)
	if e != nil {
		return nil, e
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	tap := &cloudWireTap{trackFrames: protocol == "gemini"}
	httpClient := a.cloudHTTPClient(src, max, tap)
	var stream bool
	_ = json.Unmarshal(body["stream"], &stream)
	if src.Provider == "bedrock" {
		client, e := f.Bedrock(ctx, c, httpClient)
		if e != nil {
			return nil, &cloudBeforeSendError{Message: "AWS profile 初始化失败"}
		}
		if stream {
			return cloudBedrockStream(ctx, client, bedrock, max, tap)
		}
		out, e := client.Converse(ctx, bedrock)
		if e != nil {
			return cloudSDKFailure(ctx, e, tap.sent.Load())
		}
		return cloudBedrockJSON(out, aws.ToString(bedrock.ModelId), max)
	}
	client, e := f.Vertex(ctx, c, httpClient, a.Secrets, src.CredentialRef)
	if e != nil {
		return nil, &cloudBeforeSendError{Message: "Google 凭据初始化失败"}
	}
	model := cloudString(body["model"])
	if stream {
		return cloudVertexStream(ctx, client, model, vertex, max, protocol, tap)
	}
	out, e := client.GenerateContent(ctx, model, vertex.Contents, vertex.Config)
	if e != nil {
		return cloudSDKFailure(ctx, e, tap.sent.Load())
	}
	return cloudVertexJSON(out, model, max, protocol, tap)
}

type cloudBeforeSendError struct{ Message string }

func (e *cloudBeforeSendError) Error() string { return e.Message }
func cloudSDKFailure(ctx context.Context, e error, sent bool) (*http.Response, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if !sent {
		return nil, &cloudBeforeSendError{Message: "云请求尚未提交，SDK 凭据或请求初始化失败"}
	}
	status := 502
	var se *smithyhttp.ResponseError
	if errors.As(e, &se) {
		status = se.HTTPStatusCode()
	}
	var ge genai.APIError
	if errors.As(e, &ge) {
		status = ge.Code
	}
	var api smithy.APIError
	if errors.As(e, &api) {
		switch api.ErrorCode() {
		case "AccessDeniedException":
			status = 403
		case "ThrottlingException":
			status = 429
		case "ValidationException":
			status = 422
		case "ResourceNotFoundException":
			status = 404
		}
	}
	if status < 400 || status > 599 {
		status = 502
	}
	return cloudHTTPResponse([]byte(encode(map[string]any{"error": map[string]string{"message": "云 provider 请求失败，未自动重放生成请求", "type": "upstream_error"}})), "application/json", status), nil
}
func cloudHTTPResponse(raw []byte, mime string, status int) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{mime}}, Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw))}
}
func cloudString(raw json.RawMessage) string { var v string; _ = json.Unmarshal(raw, &v); return v }

type cloudVertexRequest struct {
	Contents []*genai.Content
	Config   *genai.GenerateContentConfig
}

func cloudPrepare(provider string, c *CloudSelection, body map[string]json.RawMessage, protocol string) (*bedrockruntime.ConverseInput, *cloudVertexRequest, error) {
	invalid := func(message string) (*bedrockruntime.ConverseInput, *cloudVertexRequest, error) {
		return nil, nil, &accountingError{Status: 422, Field: "cloud", Message: message}
	}
	if e := validateCloudSelection(provider, c); e != nil {
		return nil, nil, e
	}
	model := cloudString(body["model"])
	if model == "" || len(model) > 2048 {
		return invalid("云 model 不能为空")
	}
	if protocol != "responses" && !(provider == "vertex" && protocol == "gemini") {
		return invalid("云 adapter 只接规范化 Responses 或 Vertex 原生 Gemini")
	}
	if provider == "vertex" && (!strings.HasPrefix(model, "gemini-") || strings.ContainsAny(model, "/\\?#\r\n")) {
		return invalid("此 Vertex 卡只支持 Gemini 模型")
	}
	if protocol == "gemini" {
		v, e := cloudNativeVertex(body)
		if e != nil {
			return nil, nil, e
		}
		return nil, v, nil
	}
	allowed := []string{"model", "input", "instructions", "stream", "store", "tools", "tool_choice", "parallel_tool_calls", "max_output_tokens", "temperature", "top_p", "additionalModelRequestFields"}
	for k := range body {
		if !slices.Contains(allowed, k) {
			return invalid("云文本/function 子集不支持请求字段 " + k)
		}
	}
	if raw := body["store"]; len(raw) > 0 && string(raw) != "false" && string(raw) != "null" {
		return invalid("云 adapter 不支持 provider 状态存储")
	}
	if raw := body["parallel_tool_calls"]; len(raw) > 0 && string(raw) != "true" {
		return invalid("云 adapter 不能保证禁用并行工具")
	}
	if len(body["additionalModelRequestFields"]) > 0 && provider != "bedrock" {
		return invalid("额外模型参数仅由 Bedrock 原生适配接受")
	}
	stripped := map[string]json.RawMessage{}
	for k, v := range body {
		if k != "additionalModelRequestFields" {
			stripped[k] = v
		}
	}
	chat, e := responsesToNative(stripped, "chat_completions")
	if e != nil {
		return invalid(e.Error())
	}
	var messages []struct {
		Role       string          `json:"role"`
		Content    json.RawMessage `json:"content"`
		ToolCallID string          `json:"tool_call_id"`
		ToolCalls  []struct {
			ID       string `json:"id"`
			Function struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"function"`
		} `json:"tool_calls"`
	}
	if json.Unmarshal(chat["messages"], &messages) != nil {
		return invalid("云消息无法转换")
	}
	b := &bedrockruntime.ConverseInput{ModelId: aws.String(model)}
	v := &cloudVertexRequest{Config: &genai.GenerateContentConfig{HTTPOptions: &genai.HTTPOptions{RetryOptions: &genai.HTTPRetryOptions{Attempts: genai.Ptr(int32(1))}}}}
	calls := map[string]string{}
	resolved := map[string]bool{}
	addB := func(role bt.ConversationRole, parts []bt.ContentBlock) {
		if len(parts) == 0 {
			return
		}
		if len(b.Messages) > 0 && b.Messages[len(b.Messages)-1].Role == role {
			b.Messages[len(b.Messages)-1].Content = append(b.Messages[len(b.Messages)-1].Content, parts...)
		} else {
			b.Messages = append(b.Messages, bt.Message{Role: role, Content: parts})
		}
	}
	addV := func(role string, parts []*genai.Part) {
		if len(parts) == 0 {
			return
		}
		if len(v.Contents) > 0 && v.Contents[len(v.Contents)-1].Role == role {
			v.Contents[len(v.Contents)-1].Parts = append(v.Contents[len(v.Contents)-1].Parts, parts...)
		} else {
			v.Contents = append(v.Contents, &genai.Content{Role: role, Parts: parts})
		}
	}
	for _, m := range messages {
		text := cloudString(m.Content)
		if m.Role == "system" {
			b.System = append(b.System, &bt.SystemContentBlockMemberText{Value: text})
			if v.Config.SystemInstruction == nil {
				v.Config.SystemInstruction = &genai.Content{Parts: []*genai.Part{}}
			}
			v.Config.SystemInstruction.Parts = append(v.Config.SystemInstruction.Parts, &genai.Part{Text: text})
			continue
		}
		bp := []bt.ContentBlock{}
		vp := []*genai.Part{}
		role := bt.ConversationRole(m.Role)
		vr := m.Role
		if vr == "assistant" {
			vr = "model"
		}
		if m.Role == "tool" {
			name, exists := calls[m.ToolCallID]
			if !exists || resolved[m.ToolCallID] {
				return invalid("工具结果缺少唯一且在前的调用 ID")
			}
			resolved[m.ToolCallID] = true
			bp = append(bp, &bt.ContentBlockMemberToolResult{Value: bt.ToolResultBlock{ToolUseId: aws.String(m.ToolCallID), Content: []bt.ToolResultContentBlock{&bt.ToolResultContentBlockMemberText{Value: text}}}})
			vp = append(vp, &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: m.ToolCallID, Name: name, Response: map[string]any{"result": text}}})
			role = bt.ConversationRoleUser
			vr = "user"
		} else {
			if m.Role != "user" && m.Role != "assistant" {
				return invalid("云消息角色无法转换")
			}
			if text != "" {
				bp = append(bp, &bt.ContentBlockMemberText{Value: text})
				vp = append(vp, &genai.Part{Text: text})
			}
			for _, call := range m.ToolCalls {
				if call.ID == "" || call.Function.Name == "" || calls[call.ID] != "" {
					return invalid("工具调用 ID 或名称无效/重复")
				}
				var args map[string]any
				if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil || args == nil {
					return invalid("工具参数必须是 JSON 对象")
				}
				calls[call.ID] = call.Function.Name
				bp = append(bp, &bt.ContentBlockMemberToolUse{Value: bt.ToolUseBlock{ToolUseId: aws.String(call.ID), Name: aws.String(call.Function.Name), Input: document.NewLazyDocument(args)}})
				vp = append(vp, &genai.Part{FunctionCall: &genai.FunctionCall{ID: call.ID, Name: call.Function.Name, Args: args}})
			}
		}
		addB(role, bp)
		addV(vr, vp)
	}
	if len(b.Messages) == 0 {
		return invalid("至少需要一条文本/工具消息")
	}
	if raw := body["max_output_tokens"]; len(raw) > 0 {
		var n int64
		if json.Unmarshal(raw, &n) != nil || n <= 0 || n > math.MaxInt32 {
			return invalid("max_output_tokens 无效")
		}
		if b.InferenceConfig == nil {
			b.InferenceConfig = &bt.InferenceConfiguration{}
		}
		b.InferenceConfig.MaxTokens = aws.Int32(int32(n))
		v.Config.MaxOutputTokens = int32(n)
	}
	for _, p := range []struct {
		key     string
		vertex  **float32
		bedrock **float32
	}{{"temperature", &v.Config.Temperature, nil}, {"top_p", &v.Config.TopP, nil}} {
		if raw := body[p.key]; len(raw) > 0 {
			var n float32
			if json.Unmarshal(raw, &n) != nil || math.IsNaN(float64(n)) || math.IsInf(float64(n), 0) || n < 0 {
				return invalid("采样参数无效")
			}
			*p.vertex = &n
			if b.InferenceConfig == nil {
				b.InferenceConfig = &bt.InferenceConfiguration{}
			}
			if p.key == "temperature" {
				b.InferenceConfig.Temperature = &n
			} else {
				b.InferenceConfig.TopP = &n
			}
		}
	}
	var tools []struct {
		Type        string         `json:"type"`
		Name        string         `json:"name"`
		Description string         `json:"description"`
		Parameters  map[string]any `json:"parameters"`
		Strict      *bool          `json:"strict"`
	}
	if len(body["tools"]) > 0 && json.Unmarshal(body["tools"], &tools) != nil {
		return invalid("工具定义无效")
	}
	names := map[string]bool{}
	for _, tool := range tools {
		if tool.Type != "function" || tool.Name == "" || names[tool.Name] || tool.Parameters == nil {
			return invalid("只支持唯一命名的 function 工具")
		}
		if provider == "vertex" && tool.Strict != nil && *tool.Strict {
			return invalid("Vertex 此工具转换不能兑现 strict:true")
		}
		names[tool.Name] = true
		if b.ToolConfig == nil {
			b.ToolConfig = &bt.ToolConfiguration{}
		}
		b.ToolConfig.Tools = append(b.ToolConfig.Tools, &bt.ToolMemberToolSpec{Value: bt.ToolSpecification{Name: aws.String(tool.Name), Description: aws.String(tool.Description), InputSchema: &bt.ToolInputSchemaMemberJson{Value: document.NewLazyDocument(tool.Parameters)}, Strict: tool.Strict}})
		if len(v.Config.Tools) == 0 {
			v.Config.Tools = []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{}}}
		}
		v.Config.Tools[0].FunctionDeclarations = append(v.Config.Tools[0].FunctionDeclarations, &genai.FunctionDeclaration{Name: tool.Name, Description: tool.Description, ParametersJsonSchema: tool.Parameters})
	}
	if raw := body["tool_choice"]; len(raw) > 0 {
		choice := cloudString(raw)
		var named struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if choice == "" && json.Unmarshal(raw, &named) == nil && named.Type == "function" && names[named.Name] {
			choice = "named"
		}
		if len(tools) == 0 {
			return invalid("tool_choice 需要已声明工具")
		}
		v.Config.ToolConfig = &genai.ToolConfig{FunctionCallingConfig: &genai.FunctionCallingConfig{}}
		switch choice {
		case "auto":
			b.ToolConfig.ToolChoice = &bt.ToolChoiceMemberAuto{Value: bt.AutoToolChoice{}}
			v.Config.ToolConfig.FunctionCallingConfig.Mode = genai.FunctionCallingConfigModeAuto
		case "required":
			b.ToolConfig.ToolChoice = &bt.ToolChoiceMemberAny{Value: bt.AnyToolChoice{}}
			v.Config.ToolConfig.FunctionCallingConfig.Mode = genai.FunctionCallingConfigModeAny
		case "named":
			b.ToolConfig.ToolChoice = &bt.ToolChoiceMemberTool{Value: bt.SpecificToolChoice{Name: aws.String(named.Name)}}
			v.Config.ToolConfig.FunctionCallingConfig.Mode = genai.FunctionCallingConfigModeAny
			v.Config.ToolConfig.FunctionCallingConfig.AllowedFunctionNames = []string{named.Name}
		case "none":
			if provider == "bedrock" {
				return invalid("此 Bedrock 卡不能兑现 tool_choice:none")
			}
			v.Config.ToolConfig.FunctionCallingConfig.Mode = genai.FunctionCallingConfigModeNone
		default:
			return invalid("tool_choice 无法转换")
		}
	}
	if raw := body["additionalModelRequestFields"]; len(raw) > 0 {
		var fields map[string]any
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			return invalid("additionalModelRequestFields 必须是原生 JSON 对象")
		}
		b.AdditionalModelRequestFields = document.NewLazyDocument(fields)
	}
	return b, v, nil
}

func cloudNativeVertex(body map[string]json.RawMessage) (*cloudVertexRequest, error) {
	bad := func(message string) (*cloudVertexRequest, error) {
		return nil, &accountingError{Status: 422, Field: "cloud", Message: message}
	}
	allowed := []string{"model", "stream", "contents", "systemInstruction", "generationConfig", "tools", "toolConfig", "safetySettings"}
	for k := range body {
		if !slices.Contains(allowed, k) {
			return bad("Vertex 基础 Gemini 卡不支持字段 " + k)
		}
	}
	parts, e := geminiParts(body)
	if e != nil {
		return bad(e.Error())
	}
	for _, raw := range parts {
		var p map[string]json.RawMessage
		if json.Unmarshal(raw, &p) != nil {
			return bad("Gemini part 无效")
		}
		for k := range p {
			if !slices.Contains([]string{"text", "functionCall", "functionResponse", "thought", "thoughtSignature"}, k) {
				return bad("Vertex 此卡不支持多模态/服务器工具 part")
			}
		}
	}
	v := &cloudVertexRequest{Config: &genai.GenerateContentConfig{}}
	if strictJSON(body["contents"], &v.Contents) != nil || len(v.Contents) == 0 {
		return bad("Vertex contents 无效")
	}
	var calls []*genai.FunctionCall
	seenIDs := map[string]bool{}
	used := map[int]bool{}
	for _, content := range v.Contents {
		if content == nil || (content.Role != "user" && content.Role != "model") {
			return bad("Gemini role 无效")
		}
		for _, p := range content.Parts {
			if p == nil {
				return bad("Gemini part 无效")
			}
			if call := p.FunctionCall; call != nil {
				if content.Role != "model" || call.Name == "" || len(call.PartialArgs) > 0 || call.WillContinue != nil {
					return bad("请求 history 必须包含完整工具调用")
				}
				if call.ID != "" {
					if seenIDs[call.ID] {
						return bad("重复工具调用 ID")
					}
					seenIDs[call.ID] = true
				}
				calls = append(calls, call)
			}
			if response := p.FunctionResponse; response != nil {
				if content.Role != "user" || response.Name == "" || response.Response == nil || len(response.Parts) > 0 || response.WillContinue != nil || response.Scheduling != "" {
					return bad("工具结果必须是 user 文本 JSON 结果")
				}
				matched := -1
				for index, call := range calls {
					// Native Gemini IDs are optional; a named result without an ID
					// can join only one unresolved call that also omitted its ID.
					if !used[index] && call.ID == response.ID && call.Name == response.Name {
						if matched != -1 {
							return bad("无 ID 工具结果对应多个前置调用")
						}
						matched = index
					}
				}
				if matched == -1 {
					return bad("工具结果与前置调用 ID/name 不一致")
				}
				used[matched] = true
			}
		}
	}
	merged := map[string]json.RawMessage{}
	if raw := body["generationConfig"]; len(raw) > 0 {
		if json.Unmarshal(raw, &merged) != nil {
			return bad("generationConfig 无效")
		}
		allowed := []string{"temperature", "topP", "topK", "maxOutputTokens", "stopSequences", "candidateCount"}
		for k := range merged {
			if !slices.Contains(allowed, k) {
				return bad("Vertex 此卡不支持 generationConfig 字段 " + k)
			}
		}
	}
	for _, k := range []string{"systemInstruction", "tools", "toolConfig", "safetySettings"} {
		if len(body[k]) > 0 {
			merged[k] = body[k]
		}
	}
	raw, e := json.Marshal(merged)
	if e != nil {
		return bad("Vertex config 无效")
	}
	if strictJSON(raw, v.Config) != nil {
		return bad("Vertex config 无效")
	}
	if v.Config.CandidateCount > 1 {
		return bad("Vertex 此转换只支持单候选结果")
	}
	for _, tool := range v.Config.Tools {
		if tool == nil || len(tool.FunctionDeclarations) == 0 {
			return bad("Vertex 此卡只支持 functionDeclarations")
		}
		m := map[string]json.RawMessage{}
		b, _ := json.Marshal(tool)
		json.Unmarshal(b, &m)
		for k := range m {
			if k != "functionDeclarations" {
				return bad("Vertex 服务器工具未支持")
			}
		}
	}
	if v.Config.ToolConfig != nil && (v.Config.ToolConfig.RetrievalConfig != nil || v.Config.ToolConfig.IncludeServerSideToolInvocations != nil) {
		return bad("Vertex 服务器工具未支持")
	}
	v.Config.HTTPOptions = &genai.HTTPOptions{RetryOptions: &genai.HTTPRetryOptions{Attempts: genai.Ptr(int32(1))}}
	return v, nil
}
func cloudBedrockUsage(n *nativeOutput, u *bt.TokenUsage) error {
	if u == nil {
		return nil
	}
	for _, p := range []struct {
		value *int32
		to    **int64
	}{{u.InputTokens, &n.usage.Input}, {u.OutputTokens, &n.usage.Output}, {u.CacheReadInputTokens, &n.usage.Cached}, {u.CacheWriteInputTokens, &n.usage.CacheCreation}} {
		if p.value != nil {
			if *p.value < 0 {
				return errors.New("Bedrock usage 无效")
			}
			v := int64(*p.value)
			*p.to = &v
		}
	}
	return nil
}
func cloudBedrockFinish(reason bt.StopReason) string {
	switch reason {
	case bt.StopReasonEndTurn, bt.StopReasonStopSequence:
		return "stop"
	case bt.StopReasonToolUse:
		return "tool_use"
	case bt.StopReasonMaxTokens:
		return "max_tokens"
	case bt.StopReasonContentFiltered, bt.StopReasonGuardrailIntervened:
		return "content_filter"
	default:
		return ""
	}
}
func cloudBedrockJSON(out *bedrockruntime.ConverseOutput, model string, max int64) (*http.Response, error) {
	if out == nil {
		return nil, errors.New("Bedrock 空响应")
	}
	n := newNativeOutput("cloud", max, true)
	if e := n.start(id("resp_cove"), model); e != nil {
		return nil, e
	}
	message, ok := out.Output.(*bt.ConverseOutputMemberMessage)
	if !ok || message.Value.Role != bt.ConversationRoleAssistant {
		return nil, errors.New("Bedrock 返回内容无法转换")
	}
	ids := map[string]bool{}
	for i, block := range message.Value.Content {
		switch block := block.(type) {
		case *bt.ContentBlockMemberText:
			oi, e := n.add(i, "message", "", "", "")
			if e != nil {
				return nil, e
			}
			if e = n.delta(oi, block.Value); e != nil {
				return nil, e
			}
		case *bt.ContentBlockMemberToolUse:
			cid, name := aws.ToString(block.Value.ToolUseId), aws.ToString(block.Value.Name)
			if cid == "" || ids[cid] || block.Value.Input == nil {
				return nil, errors.New("Bedrock 工具 ID/参数缺失或重复")
			}
			ids[cid] = true
			var args map[string]any
			if e := block.Value.Input.UnmarshalSmithyDocument(&args); e != nil || args == nil {
				return nil, errors.New("Bedrock 工具参数不是 JSON 对象")
			}
			oi, e := n.add(i, "function_call", cid, name, "")
			if e != nil {
				return nil, e
			}
			if e = n.delta(oi, encode(args)); e != nil {
				return nil, e
			}
		default:
			return nil, errors.New("Bedrock 返回未支持的多模态/opaque block")
		}
	}
	if e := cloudBedrockUsage(n, out.Usage); e != nil {
		return nil, e
	}
	n.finish = cloudBedrockFinish(out.StopReason)
	if e := n.complete(); e != nil {
		return nil, e
	}
	raw := []byte(encode(n.response))
	if int64(len(raw)) > max {
		return nil, errors.New("云响应超过限制")
	}
	return cloudHTTPResponse(raw, "application/json", 200), nil
}

type cloudEventBody struct {
	mu               sync.Mutex
	ctx              context.Context
	cancel           context.CancelFunc
	next             func() ([]byte, error)
	stop             func()
	buffer           []byte
	finished, closed bool
}

func (b *cloudEventBody) Read(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0, io.ErrClosedPipe
	}
	for len(b.buffer) == 0 {
		if b.ctx.Err() != nil {
			return 0, b.ctx.Err()
		}
		if b.finished {
			return 0, io.EOF
		}
		raw, e := b.next()
		if e != nil {
			b.finished = true
			return 0, e
		}
		b.buffer = raw
	}
	n := copy(p, b.buffer)
	b.buffer = b.buffer[n:]
	return n, nil
}
func (b *cloudEventBody) Close() error {
	b.cancel()
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.closed {
		b.closed = true
		if b.stop != nil {
			b.stop()
		}
	}
	b.buffer = nil
	return nil
}
func cloudStreamResponse(body *cloudEventBody) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body, ContentLength: -1}
}
func cloudBedrockStream(ctx context.Context, client cloudBedrockAPI, input *bedrockruntime.ConverseInput, max int64, tap *cloudWireTap) (*http.Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	out, e := client.ConverseStream(ctx, &bedrockruntime.ConverseStreamInput{ModelId: input.ModelId, Messages: input.Messages, System: input.System, InferenceConfig: input.InferenceConfig, ToolConfig: input.ToolConfig, AdditionalModelRequestFields: input.AdditionalModelRequestFields})
	if e != nil {
		response, failure := cloudSDKFailure(ctx, e, tap.sent.Load())
		cancel()
		return response, failure
	}
	if out == nil || out.GetStream() == nil {
		cancel()
		return nil, errors.New("Bedrock eventstream 缺失")
	}
	stream := out.GetStream()
	n := newNativeOutput("cloud", max, true)
	blocks := map[int]bool{}
	ids := map[string]bool{}
	messageStopped := false
	metadataSeen := false
	body := &cloudEventBody{ctx: ctx, cancel: cancel, stop: func() { _ = stream.Close() }}
	body.next = func() ([]byte, error) {
		var wire []byte
		n.emit = func(frame []byte) error { wire = append(wire, frame...); return nil }
		for len(wire) == 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case event, ok := <-stream.Events():
				if !ok {
					if e := stream.Err(); e != nil {
						return nil, errors.New("Bedrock eventstream 中断")
					}
					if !messageStopped {
						return nil, errors.New("Bedrock 缺少 messageStop")
					}
					if !n.terminal {
						if e := n.complete(); e != nil {
							return nil, e
						}
						return wire, nil
					}
					return nil, io.EOF
				}
				switch event := event.(type) {
				case *bt.ConverseStreamOutputMemberMessageStart:
					if n.started || event.Value.Role != bt.ConversationRoleAssistant {
						return nil, errors.New("Bedrock messageStart 顺序无效")
					}
					if e := n.start(id("resp_cove"), aws.ToString(input.ModelId)); e != nil {
						return nil, e
					}
				case *bt.ConverseStreamOutputMemberContentBlockStart:
					if !n.started || messageStopped {
						return nil, errors.New("Bedrock blockStart 顺序无效")
					}
					index := int(aws.ToInt32(event.Value.ContentBlockIndex))
					if event.Value.ContentBlockIndex == nil || index < 0 {
						return nil, errors.New("Bedrock block index 无效")
					}
					if _, exists := blocks[index]; exists {
						return nil, errors.New("Bedrock 重复 blockStart")
					}
					blocks[index] = true
					switch start := event.Value.Start.(type) {
					case *bt.ContentBlockStartMemberToolUse:
						cid, name := aws.ToString(start.Value.ToolUseId), aws.ToString(start.Value.Name)
						if cid == "" || ids[cid] {
							return nil, errors.New("Bedrock 工具 ID 缺失/重复")
						}
						ids[cid] = true
						if _, e := n.add(index, "function_call", cid, name, ""); e != nil {
							return nil, e
						}
					default:
						return nil, errors.New("Bedrock blockStart 类型未支持")
					}
				case *bt.ConverseStreamOutputMemberContentBlockDelta:
					if !n.started || messageStopped || event.Value.ContentBlockIndex == nil {
						return nil, errors.New("Bedrock delta 顺序无效")
					}
					index := int(*event.Value.ContentBlockIndex)
					if index < 0 {
						return nil, errors.New("Bedrock block index 无效")
					}
					oi, exists := n.indices[index]
					switch delta := event.Value.Delta.(type) {
					case *bt.ContentBlockDeltaMemberText:
						if !exists {
							if _, closed := blocks[index]; closed {
								return nil, errors.New("Bedrock 结束后的 text delta")
							}
							blocks[index] = true
							var e error
							oi, e = n.add(index, "message", "", "", "")
							if e != nil {
								return nil, e
							}
						}
						if !blocks[index] || n.items[oi]["type"] != "message" {
							return nil, errors.New("Bedrock text delta 类型/状态错误")
						}
						if e := n.delta(oi, delta.Value); e != nil {
							return nil, e
						}
					case *bt.ContentBlockDeltaMemberToolUse:
						if !exists || !blocks[index] || n.items[oi]["type"] != "function_call" {
							return nil, errors.New("Bedrock 工具 delta 早于 start")
						}
						if e := n.delta(oi, aws.ToString(delta.Value.Input)); e != nil {
							return nil, e
						}
					default:
						return nil, errors.New("Bedrock delta 类型未支持")
					}
				case *bt.ConverseStreamOutputMemberContentBlockStop:
					if event.Value.ContentBlockIndex == nil {
						return nil, errors.New("Bedrock blockStop 缺少 index")
					}
					index := int(*event.Value.ContentBlockIndex)
					if !blocks[index] || messageStopped {
						return nil, errors.New("Bedrock blockStop 顺序无效")
					}
					blocks[index] = false
				case *bt.ConverseStreamOutputMemberMessageStop:
					if !n.started || messageStopped {
						return nil, errors.New("Bedrock messageStop 顺序无效")
					}
					for _, open := range blocks {
						if open {
							return nil, errors.New("Bedrock messageStop 早于 blockStop")
						}
					}
					messageStopped = true
					n.finish = cloudBedrockFinish(event.Value.StopReason)
				case *bt.ConverseStreamOutputMemberMetadata:
					if !messageStopped || metadataSeen {
						return nil, errors.New("Bedrock metadata 顺序无效")
					}
					metadataSeen = true
					if e := cloudBedrockUsage(n, event.Value.Usage); e != nil {
						return nil, e
					}
				default:
					return nil, errors.New("Bedrock 未支持的 stream event")
				}
			}
		}
		return wire, nil
	}
	return cloudStreamResponse(body), nil
}
func cloudVertexWire(out *genai.GenerateContentResponse, tap *cloudWireTap) ([]byte, error) {
	if out == nil {
		return nil, errors.New("Vertex 空响应")
	}
	raw, e := json.Marshal(out)
	if e != nil {
		return nil, e
	}
	var wire map[string]any
	if json.Unmarshal(raw, &wire) != nil {
		return nil, errors.New("Vertex 响应无法编码")
	}
	delete(wire, "sdkHttpResponse")
	u, e := tap.frame()
	if e != nil {
		return nil, e
	}
	metadata, _ := wire["usageMetadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	for _, p := range []struct {
		k string
		v *int64
	}{{"promptTokenCount", u.Input}, {"candidatesTokenCount", u.Output}, {"cachedContentTokenCount", u.Cached}, {"thoughtsTokenCount", u.Reasoning}} {
		delete(metadata, p.k)
		if p.v != nil {
			metadata[p.k] = *p.v
		}
	}
	if len(metadata) > 0 {
		wire["usageMetadata"] = metadata
	} else {
		delete(wire, "usageMetadata")
	}
	return []byte(encode(wire)), nil
}
func cloudVertexFinish(reason genai.FinishReason) string {
	switch reason {
	case genai.FinishReasonStop:
		return "stop"
	case genai.FinishReasonMaxTokens:
		return "max_tokens"
	case genai.FinishReasonSafety, genai.FinishReasonRecitation, genai.FinishReasonBlocklist, genai.FinishReasonProhibitedContent:
		return "content_filter"
	default:
		return ""
	}
}
func cloudVertexParts(n *nativeOutput, out *genai.GenerateContentResponse, stream bool, state map[string]int) error {
	if out == nil || len(out.Candidates) > 1 {
		return errors.New("Vertex 非单候选结果")
	}
	if !n.started {
		rid := out.ResponseID
		if rid == "" {
			rid = id("resp_cove")
		}
		if e := n.start(rid, out.ModelVersion); e != nil {
			return e
		}
	}
	if len(out.Candidates) == 0 {
		if out.PromptFeedback != nil && out.PromptFeedback.BlockReason != "" {
			n.finish = "content_filter"
		}
		return nil
	}
	candidate := out.Candidates[0]
	if candidate == nil {
		return errors.New("Vertex 空 candidate")
	}
	if candidate.Content != nil {
		for _, part := range candidate.Content.Parts {
			if part == nil {
				return errors.New("Vertex 空 part")
			}
			if part.Thought || len(part.ThoughtSignature) > 0 || part.InlineData != nil || part.FileData != nil || part.ToolCall != nil || part.ToolResponse != nil || part.ExecutableCode != nil || part.CodeExecutionResult != nil {
				return errors.New("Vertex opaque/多模态内容不可转换到 Responses")
			}
			if part.Text != "" {
				oi, exists := state["text"]
				if !exists {
					var e error
					oi, e = n.add(len(n.items), "message", "", "", "")
					if e != nil {
						return e
					}
					state["text"] = oi
				}
				if e := n.delta(oi, part.Text); e != nil {
					return e
				}
			}
			if call := part.FunctionCall; call != nil {
				if call.Name == "" {
					return errors.New("Vertex 工具名称缺失")
				}
				if len(call.PartialArgs) > 0 {
					return errors.New("Vertex partialArgs JSONPath 暂不能映射到 ordered JSON 字节增量")
				}
				cid := call.ID
				if cid == "" {
					cid = id("call_cove")
				}
				key := "call:" + cid
				oi, exists := state[key]
				if exists {
					return errors.New("Vertex 重复工具 ID 或分块格式未声明")
				}
				var e error
				oi, e = n.add(len(n.items), "function_call", cid, call.Name, "")
				if e != nil {
					return e
				}
				state[key] = oi
				args := call.Args
				if args == nil {
					args = map[string]any{}
				}
				if call.WillContinue != nil && *call.WillContinue {
					return errors.New("Vertex 未完成工具参数不能伪造完整 JSON")
				}
				if e = n.delta(oi, encode(args)); e != nil {
					return e
				}
			}
		}
	}
	if candidate.FinishReason != "" {
		if n.finish != "" {
			return errors.New("Vertex 重复生成终态")
		}
		n.finish = cloudVertexFinish(candidate.FinishReason)
		if n.finish == "" {
			return errors.New("Vertex 未支持的结束原因")
		}
	}
	return nil
}
func cloudVertexJSON(out *genai.GenerateContentResponse, model string, max int64, protocol string, tap *cloudWireTap) (*http.Response, error) {
	if protocol == "gemini" {
		raw, e := cloudVertexWire(out, tap)
		if e != nil {
			return nil, e
		}
		if int64(len(raw)) > max {
			return nil, errors.New("云响应超过限制")
		}
		return cloudHTTPResponse(raw, "application/json", 200), nil
	}
	n := newNativeOutput("cloud", max, true)
	n.response["model"] = model
	if e := cloudVertexParts(n, out, false, map[string]int{}); e != nil {
		return nil, e
	}
	u, e := tap.current()
	if e != nil {
		return nil, e
	}
	n.usage = u
	if e = n.complete(); e != nil {
		return nil, e
	}
	raw := []byte(encode(n.response))
	if int64(len(raw)) > max {
		return nil, errors.New("云响应超过限制")
	}
	return cloudHTTPResponse(raw, "application/json", 200), nil
}
func cloudVertexStream(ctx context.Context, client cloudVertexAPI, model string, v *cloudVertexRequest, max int64, protocol string, tap *cloudWireTap) (*http.Response, error) {
	ctx, cancel := context.WithCancel(ctx)
	next, stop := iter.Pull2(client.GenerateContentStream(ctx, model, v.Contents, v.Config))
	first, e, ok := next()
	if e != nil {
		response, failure := cloudSDKFailure(ctx, e, tap.sent.Load())
		stop()
		cancel()
		return response, failure
	}
	if !ok {
		stop()
		cancel()
		return nil, errors.New("Vertex 空生成 stream")
	}
	n := newNativeOutput("cloud", max, true)
	n.response["model"] = model
	state := map[string]int{}
	body := &cloudEventBody{ctx: ctx, cancel: cancel, stop: stop}
	pending := first
	body.next = func() ([]byte, error) {
		for {
			out := pending
			pending = nil
			if out == nil {
				var e error
				var ok bool
				out, e, ok = next()
				if e != nil {
					return nil, errors.New("Vertex stream 中断")
				}
				if !ok {
					if protocol == "gemini" {
						return nil, io.EOF
					}
					if n.terminal {
						return nil, io.EOF
					}
					u, e := tap.current()
					if e != nil {
						return nil, e
					}
					n.usage = u
					var frames []byte
					n.emit = func(frame []byte) error { frames = append(frames, frame...); return nil }
					if e = n.complete(); e != nil {
						return nil, e
					}
					return frames, nil
				}
			}
			if protocol == "gemini" {
				raw, e := cloudVertexWire(out, tap)
				if e != nil {
					return nil, e
				}
				return []byte("data: " + string(raw) + "\n\n"), nil
			}
			var frames []byte
			n.emit = func(frame []byte) error { frames = append(frames, frame...); return nil }
			if e := cloudVertexParts(n, out, true, state); e != nil {
				return nil, e
			}
			if len(frames) > 0 {
				return frames, nil
			}
		}
	}
	return cloudStreamResponse(body), nil
}
