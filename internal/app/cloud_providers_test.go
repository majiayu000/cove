package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/auth"
	"cloud.google.com/go/auth/httptransport"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	awsc "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"google.golang.org/genai"
)

func cloudBedrockSelection() *CloudSelection {
	return &CloudSelection{AWSRegion: "us-east-1", AWSProfile: "explicit-synthetic"}
}
func cloudVertexSelection() *CloudSelection {
	return &CloudSelection{VertexProject: "synthetic-project", VertexLocation: "us-central1", VertexCredentialsMode: "adc"}
}
func cloudBody(raw string) map[string]json.RawMessage {
	var b map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &b) != nil {
		panic("fixture JSON")
	}
	return b
}
func cloudMockFactories(t *testing.T) cloudSDKFactories {
	t.Helper()
	return cloudSDKFactories{
		Bedrock: func(ctx context.Context, c *CloudSelection, h *http.Client) (cloudBedrockAPI, error) {
			return bedrockruntime.New(bedrockruntime.Options{Region: c.AWSRegion, BaseEndpoint: aws.String(cloudEndpoint("bedrock", c)), Credentials: awsc.NewStaticCredentialsProvider("SYNTHETIC_ACCESS", "SYNTHETIC_SIGNING_SECRET", ""), HTTPClient: h, Retryer: aws.NopRetryer{}, RetryMaxAttempts: 1}), nil
		},
		Vertex: func(ctx context.Context, c *CloudSelection, h *http.Client, v Secrets, ref string) (cloudVertexAPI, error) {
			creds := auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: cloudSyntheticToken{}})
			if e := httptransport.AddAuthorizationMiddleware(h, creds); e != nil {
				return nil, e
			}
			client, e := genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: c.VertexProject, Location: c.VertexLocation, Credentials: creds, HTTPClient: h, HTTPOptions: genai.HTTPOptions{BaseURL: cloudEndpoint("vertex", c), APIVersion: "v1", RetryOptions: &genai.HTTPRetryOptions{Attempts: genai.Ptr(int32(1))}}})
			if e != nil {
				return nil, e
			}
			return client.Models, nil
		},
	}
}

type cloudSyntheticToken struct{}

func (cloudSyntheticToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: "SYNTHETIC_GOOGLE_TOKEN", Expiry: time.Now().Add(time.Hour)}, nil
}
func cloudRead(t *testing.T, response *http.Response) (string, error) {
	t.Helper()
	if response == nil {
		t.Fatal("nil response")
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(response.Body)
	return string(raw), e
}
func TestSpecCloudBedrockSDKConverseTextToolsAndUsage(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "bedrock-runtime.us-east-1.amazonaws.com" || r.URL.EscapedPath() != "/model/anthropic.fixture/converse" || !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Fatal("wrong native endpoint/signing")
		}
		if r.GetBody != nil {
			t.Fatal("POST can be replayed")
		}
		raw, e := io.ReadAll(r.Body)
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(string(raw), `"toolUseId":"tool-one"`) || !strings.Contains(string(raw), `"toolResult"`) || !strings.Contains(string(raw), `"additionalModelRequestFields"`) {
			t.Fatal("request did not preserve tool history/native extra field", string(raw))
		}
		return contractResponse(`{"output":{"message":{"role":"assistant","content":[{"text":"synthetic"},{"toolUse":{"toolUseId":"returned-id","name":"lookup","input":{"q":"value"}}}]}},"stopReason":"tool_use","usage":{"inputTokens":7,"outputTokens":4,"totalTokens":11}}`, "application/json"), nil
	})
	src := Source{Provider: "bedrock"}
	body := cloudBody(`{"model":"anthropic.fixture","input":[{"role":"user","content":"hello"},{"type":"function_call","call_id":"tool-one","name":"lookup","arguments":"{\"q\":\"before\"}"},{"type":"function_call_output","call_id":"tool-one","output":"synthetic result"}],"tools":[{"type":"function","name":"lookup","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}],"additionalModelRequestFields":{"native_flag":true}}`)
	response, e := a.cloudUpstreamWith(context.Background(), src, cloudBedrockSelection(), body, "responses", 1<<20, cloudMockFactories(t))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := cloudRead(t, response)
	if e != nil || response.StatusCode != 200 || !strings.Contains(raw, `"call_id":"returned-id"`) || !strings.Contains(raw, `"input_tokens":7`) || calls.Load() != 1 {
		t.Fatal(e, raw, calls.Load())
	}
	if strings.Contains(raw, "SYNTHETIC_SIGNING_SECRET") || strings.Contains(raw, "SYNTHETIC_ACCESS") {
		t.Fatal("credential leaked")
	}
}
func cloudAWSFrames(t *testing.T, frames []struct{ kind, body string }) []byte {
	t.Helper()
	var wire bytes.Buffer
	encoder := eventstream.NewEncoder()
	for _, frame := range frames {
		message := eventstream.Message{Headers: eventstream.Headers{{Name: ":message-type", Value: eventstream.StringValue("event")}, {Name: ":event-type", Value: eventstream.StringValue(frame.kind)}, {Name: ":content-type", Value: eventstream.StringValue("application/json")}}, Payload: []byte(frame.body)}
		if e := encoder.Encode(&wire, message); e != nil {
			t.Fatal(e)
		}
	}
	return wire.Bytes()
}
func TestSpecCloudBedrockActualEventstreamOrderedArguments(t *testing.T) {
	frames := []struct{ kind, body string }{{"messageStart", `{"role":"assistant"}`}, {"contentBlockStart", `{"contentBlockIndex":0,"start":{"toolUse":{"toolUseId":"first-tool","name":"first"}}}`}, {"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{\"q\":"}}}`}, {"contentBlockStart", `{"contentBlockIndex":1,"start":{"toolUse":{"toolUseId":"second-tool","name":"second"}}}`}, {"contentBlockDelta", `{"contentBlockIndex":1,"delta":{"toolUse":{"input":"{\"n\":2}"}}}`}, {"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"\"ordered\"}"}}}`}, {"contentBlockStop", `{"contentBlockIndex":1}`}, {"contentBlockStop", `{"contentBlockIndex":0}`}, {"messageStop", `{"stopReason":"tool_use"}`}, {"metadata", `{"usage":{"inputTokens":0,"outputTokens":3,"totalTokens":3},"metrics":{"latencyMs":1}}`}}
	wire := cloudAWSFrames(t, frames)
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if !strings.HasSuffix(r.URL.Path, "/converse-stream") {
			t.Fatal(r.URL.Path)
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}}, Body: io.NopCloser(bytes.NewReader(wire))}, nil
	})
	response, e := a.cloudUpstreamWith(context.Background(), Source{Provider: "bedrock"}, cloudBedrockSelection(), cloudBody(`{"model":"anthropic.fixture","input":"hi","stream":true}`), "responses", 1<<20, cloudMockFactories(t))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := cloudRead(t, response)
	if e != nil {
		t.Fatal(e, raw)
	}
	if calls.Load() != 1 || !strings.Contains(raw, "response.completed") || !strings.Contains(raw, `\"q\":\"ordered\"`) || !strings.Contains(raw, `"call_id":"second-tool"`) || !strings.Contains(raw, `"input_tokens":0`) {
		t.Fatal("eventstream conversion incomplete", raw)
	}
	bad := cloudAWSFrames(t, []struct{ kind, body string }{{"messageStart", `{"role":"assistant"}`}, {"contentBlockDelta", `{"contentBlockIndex":0,"delta":{"toolUse":{"input":"{}"}}}`}})
	a.HTTP.Transport = contractTransport(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}}, Body: io.NopCloser(bytes.NewReader(bad))}, nil
	})
	response, e = a.cloudUpstreamWith(context.Background(), Source{Provider: "bedrock"}, cloudBedrockSelection(), cloudBody(`{"model":"anthropic.fixture","input":"hi","stream":true}`), "responses", 1<<20, cloudMockFactories(t))
	if e != nil {
		t.Fatal(e)
	}
	raw, e = cloudRead(t, response)
	if e == nil || strings.Contains(raw, "response.completed") {
		t.Fatal("out-of-order tool delta falsely succeeded")
	}
}
func TestSpecCloudVertexSDKPathsNativeAndUsagePresence(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if r.URL.Host != "us-central1-aiplatform.googleapis.com" || r.URL.Path != "/v1/projects/synthetic-project/locations/us-central1/publishers/google/models/gemini-fixture:generateContent" || r.Header.Get("Authorization") != "Bearer SYNTHETIC_GOOGLE_TOKEN" {
			t.Fatal("SDK project/location/auth mismatch")
		}
		return contractResponse(`{"responseId":"vertex-response","modelVersion":"gemini-fixture","candidates":[{"index":0,"content":{"role":"model","parts":[{"text":"fixture"},{"functionCall":{"id":"vertex-tool","name":"lookup","args":{"query":"x"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":2}}`, "application/json"), nil
	})
	src := Source{Provider: "vertex"}
	body := cloudBody(`{"model":"gemini-fixture","input":"synthetic"}`)
	response, e := a.cloudUpstreamWith(context.Background(), src, cloudVertexSelection(), body, "responses", 1<<20, cloudMockFactories(t))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := cloudRead(t, response)
	if e != nil || !strings.Contains(raw, `"input_tokens":0`) || !strings.Contains(raw, `"cached_tokens":null`) || !strings.Contains(raw, `"call_id":"vertex-tool"`) {
		t.Fatal(e, raw)
	}
	native := cloudBody(`{"model":"gemini-fixture","contents":[{"role":"user","parts":[{"text":"synthetic"}]}]}`)
	response, e = a.cloudUpstreamWith(context.Background(), src, cloudVertexSelection(), native, "gemini", 1<<20, cloudMockFactories(t))
	if e != nil {
		t.Fatal(e)
	}
	raw, e = cloudRead(t, response)
	if e != nil || !strings.Contains(raw, `"promptTokenCount":0`) || strings.Contains(raw, "sdkHttpResponse") || strings.Contains(raw, "SYNTHETIC_GOOGLE_TOKEN") {
		t.Fatal(e, raw)
	}
	a.HTTP.Transport = contractTransport(func(*http.Request) (*http.Response, error) {
		return contractResponse(`{"candidates":[{"content":{"role":"model","parts":[{"text":"unknown usage"}]},"finishReason":"STOP"}],"usageMetadata":{"totalTokenCount":5}}`, "application/json"), nil
	})
	response, e = a.cloudUpstreamWith(context.Background(), src, cloudVertexSelection(), body, "responses", 1<<20, cloudMockFactories(t))
	if e != nil {
		t.Fatal(e)
	}
	raw, e = cloudRead(t, response)
	if e != nil || !strings.Contains(raw, `"input_tokens":null`) || !strings.Contains(raw, `"output_tokens":null`) {
		t.Fatal("SDK zero treated as known usage", e, raw)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected SDK generation count")
	}
}
func TestSpecCloudSDKNoRetriesErrorPrivacyAndFactoryFailure(t *testing.T) {
	for _, provider := range []string{"bedrock", "vertex"} {
		t.Run(provider, func(t *testing.T) {
			var calls atomic.Int32
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				calls.Add(1)
				response := contractResponse(`{"message":"SYNTHETIC_PROVIDER_SECRET","error":{"code":429,"message":"SYNTHETIC_PROVIDER_SECRET","status":"RESOURCE_EXHAUSTED"}}`, "application/json")
				response.StatusCode = 429
				response.Header.Set("X-Amzn-Errortype", "ThrottlingException")
				return response, nil
			})
			selection := cloudBedrockSelection()
			model := "anthropic.fixture"
			if provider == "vertex" {
				selection = cloudVertexSelection()
				model = "gemini-fixture"
			}
			response, e := a.cloudUpstreamWith(context.Background(), Source{Provider: provider}, selection, cloudBody(encode(map[string]any{"model": model, "input": "test"})), "responses", 1<<20, cloudMockFactories(t))
			if e != nil {
				t.Fatal(e)
			}
			raw, e := cloudRead(t, response)
			if e != nil || response.StatusCode != 429 || calls.Load() != 1 || strings.Contains(raw, "SYNTHETIC_PROVIDER_SECRET") {
				t.Fatal(response.StatusCode, calls.Load(), e, raw)
			}
			factories := cloudMockFactories(t)
			if provider == "bedrock" {
				factories.Bedrock = func(context.Context, *CloudSelection, *http.Client) (cloudBedrockAPI, error) {
					return nil, errors.New("SYNTHETIC_PROFILE_SECRET")
				}
			} else {
				factories.Vertex = func(context.Context, *CloudSelection, *http.Client, Secrets, string) (cloudVertexAPI, error) {
					return nil, errors.New("SYNTHETIC_CREDENTIAL_SECRET")
				}
			}
			_, e = a.cloudUpstreamWith(context.Background(), Source{Provider: provider}, selection, cloudBody(encode(map[string]any{"model": model, "input": "test"})), "responses", 1<<20, factories)
			var pre *cloudBeforeSendError
			if !errors.As(e, &pre) || strings.Contains(e.Error(), "SYNTHETIC") {
				t.Fatal("factory error leaked or lost not-sent evidence", e)
			}
		})
	}
}
func TestSpecCloudBoundaryRejectsAdvancedBeforeAnySDK(t *testing.T) {
	var factories atomic.Int32
	a := contractApp(t, nil)
	f := cloudSDKFactories{Bedrock: func(context.Context, *CloudSelection, *http.Client) (cloudBedrockAPI, error) {
		factories.Add(1)
		return nil, errors.New("must not reach")
	}}
	for _, body := range []string{`{"model":"m","input":"x","background":true}`, `{"model":"m","input":"x","previous_response_id":"foreign"}`, `{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"https://example.test/secret"}]}]}`, `{"model":"m","input":"x","tools":[{"type":"web_search"}]}`, `{"model":"m","input":[{"type":"function_call_output","call_id":"missing","output":"x"}]}`, `{"model":"m","input":"x","store":true}`, `{"model":"m","input":"x","parallel_tool_calls":false}`} {
		_, e := a.cloudUpstreamWith(context.Background(), Source{Provider: "bedrock"}, cloudBedrockSelection(), cloudBody(body), "responses", 1<<20, f)
		var boundary *accountingError
		if !errors.As(e, &boundary) || boundary.Status != 422 {
			t.Fatal("unsupported boundary", e)
		}
	}
	for _, selection := range []*CloudSelection{nil, {AWSRegion: "us-east-1@metadata", AWSProfile: "selected"}, {AWSRegion: "us-east-1", AWSProfile: "../credentials"}, {VertexProject: "p", VertexLocation: "us-central1", VertexCredentialsMode: "implicit"}} {
		if e := validateCloudSelection("bedrock", selection); e == nil {
			t.Fatal("implicit/path config accepted")
		}
	}
	if factories.Load() != 0 {
		t.Fatal("rejected requests touched SDK/profile")
	}
	if cloudAuthType("bedrock", cloudBedrockSelection()) != "aws_profile" || cloudAuthType("vertex", cloudVertexSelection()) != "google_adc" {
		t.Fatal("auth kind mismatch")
	}
	for _, raw := range []string{`{"type":"external_account","private_key":"SYNTHETIC"}`, `{"type":"service_account","client_email":"synthetic","private_key":"synthetic","token_uri":"https://evil.example.test/token"}`} {
		if validateCloudServiceAccount([]byte(raw)) == nil {
			t.Fatal("unsupported credential source accepted")
		}
	}
}
func TestSpecCloudVertexStreamUsageUnknownCancelAndSize(t *testing.T) {
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		return contractResponse("data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"first\"}]}}]}\n\ndata: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"second\"}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":5,\"candidatesTokenCount\":2}}\n\n", "text/event-stream"), nil
	})
	body := cloudBody(`{"model":"gemini-fixture","input":"x","stream":true}`)
	response, e := a.cloudUpstreamWith(context.Background(), Source{Provider: "vertex"}, cloudVertexSelection(), body, "responses", 1<<20, cloudMockFactories(t))
	if e != nil {
		t.Fatal(e)
	}
	raw, e := cloudRead(t, response)
	if e != nil || !strings.Contains(raw, "response.completed") || !strings.Contains(raw, `"text":"firstsecond"`) || !strings.Contains(raw, `"input_tokens":5`) {
		t.Fatal(e, raw)
	}
	ctx, cancel := context.WithCancel(context.Background())
	response, e = a.cloudUpstreamWith(ctx, Source{Provider: "vertex"}, cloudVertexSelection(), body, "responses", 1<<20, cloudMockFactories(t))
	if e != nil {
		t.Fatal(e)
	}
	cancel()
	raw, e = cloudRead(t, response)
	if e == nil || strings.Contains(raw, "response.completed") {
		t.Fatal("cancelled stream succeeded")
	}
	a.HTTP.Transport = contractTransport(func(*http.Request) (*http.Response, error) {
		return contractResponse(strings.Repeat("x", 8192), "application/json"), nil
	})
	response, e = a.cloudUpstreamWith(context.Background(), Source{Provider: "vertex"}, cloudVertexSelection(), cloudBody(`{"model":"gemini-fixture","input":"x"}`), "responses", 1024, cloudMockFactories(t))
	if e == nil && (response == nil || response.StatusCode < 400) {
		t.Fatal("oversized cloud response accepted")
	}
	if response != nil {
		response.Body.Close()
	}
}

func TestSpecCloudVertexNativeToolRoundTripAndOrderedPartialArgs(t *testing.T) {
	var calls atomic.Int32
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		b, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(b), `"functionResponse"`) {
			t.Fatal("native tool result missing", err)
		}
		if strings.Contains(r.URL.Path, ":streamGenerateContent") {
			if !strings.Contains(string(b), `"streamFunctionCallArguments":true`) {
				t.Fatal("native streaming tool option dropped")
			}
			return contractResponse("data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"native-tool\",\"name\":\"lookup\",\"partialArgs\":[{\"jsonPath\":\"$.q\",\"stringValue\":\"first-\",\"willContinue\":true}],\"willContinue\":true}}]}}]}\n\ndata: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"functionCall\":{\"id\":\"native-tool\",\"name\":\"lookup\",\"partialArgs\":[{\"jsonPath\":\"$.q\",\"stringValue\":\"second\",\"willContinue\":false}],\"willContinue\":false}}]},\"finishReason\":\"STOP\"}],\"usageMetadata\":{\"promptTokenCount\":0,\"candidatesTokenCount\":3}}\n\n", "text/event-stream"), nil
		}
		return contractResponse(`{"candidates":[{"content":{"role":"model","parts":[{"text":"tool result accepted"}]},"finishReason":"STOP"}]}`, "application/json"), nil
	})
	body := cloudBody(`{"model":"gemini-fixture","contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"synthetic"}}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{"result":"synthetic"}}}]}]}`)
	response, err := a.cloudUpstreamWith(context.Background(), Source{Provider: "vertex"}, cloudVertexSelection(), body, "gemini", 1<<20, cloudMockFactories(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := cloudRead(t, response)
	if err != nil || !strings.Contains(raw, "tool result accepted") {
		t.Fatal(err, raw)
	}
	body["stream"] = json.RawMessage("true")
	body["toolConfig"] = json.RawMessage(`{"functionCallingConfig":{"streamFunctionCallArguments":true}}`)
	response, err = a.cloudUpstreamWith(context.Background(), Source{Provider: "vertex"}, cloudVertexSelection(), body, "gemini", 1<<20, cloudMockFactories(t))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = cloudRead(t, response)
	if err != nil || strings.Index(raw, "first-") < 0 || strings.Index(raw, "second") <= strings.Index(raw, "first-") || strings.Count(raw, `"id":"native-tool"`) != 2 || !strings.Contains(raw, `"willContinue":false`) {
		t.Fatal("native partial argument order/id lost", err, raw)
	}
	frames := strings.Split(strings.TrimSpace(raw), "\n\n")
	if len(frames) != 2 || strings.Contains(frames[0], "usageMetadata") || !strings.Contains(frames[1], `"promptTokenCount":0`) {
		t.Fatal("SDK prefetch moved usage into an earlier native event", raw)
	}
	for _, history := range []string{
		`[{"role":"model","parts":[{"functionCall":{"name":"lookup"}},{"functionCall":{"name":"lookup"}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]`,
		`[{"role":"model","parts":[{"functionCall":{"id":"required","name":"lookup"}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{}}}]}]`,
		`[{"role":"model","parts":[{"functionCall":{"id":"known","name":"lookup"}}]},{"role":"user","parts":[{"functionResponse":{"id":"other","name":"lookup","response":{}}}]}]`,
		`[{"role":"model","parts":[{"functionCall":{"name":"lookup"}}]},{"role":"user","parts":[{"functionResponse":{"name":"lookup","response":{},"parts":[{"inlineData":{"mimeType":"image/png","data":"AA=="}}]}}]}]`,
	} {
		invalid := cloudBody(`{"model":"gemini-fixture","contents":` + history + `}`)
		_, err = a.cloudUpstreamWith(context.Background(), Source{Provider: "vertex"}, cloudVertexSelection(), invalid, "gemini", 1<<20, cloudMockFactories(t))
		var boundary *accountingError
		if !errors.As(err, &boundary) || boundary.Status != 422 {
			t.Fatal("invalid native tool history accepted", err)
		}
	}
	if calls.Load() != 2 {
		t.Fatal("rejected history sent a provider POST", calls.Load())
	}
}

func TestSpecCloudBedrockCredentialFailureBeforeStreamPOST(t *testing.T) {
	a := contractApp(t, nil)
	f := cloudMockFactories(t)
	f.Bedrock = func(ctx context.Context, c *CloudSelection, h *http.Client) (cloudBedrockAPI, error) {
		return bedrockruntime.New(bedrockruntime.Options{Region: c.AWSRegion, BaseEndpoint: aws.String(cloudEndpoint("bedrock", c)), HTTPClient: h, Retryer: aws.NopRetryer{}, RetryMaxAttempts: 1, Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{}, errors.New("SYNTHETIC_CREDENTIAL_LOOKUP_FAILURE")
		})}), nil
	}
	for _, stream := range []bool{false, true} {
		_, err := a.cloudUpstreamWith(context.Background(), Source{Provider: "bedrock"}, cloudBedrockSelection(), cloudBody(encode(map[string]any{"model": "anthropic.fixture", "input": "x", "stream": stream})), "responses", 1<<20, f)
		var before *cloudBeforeSendError
		if !errors.As(err, &before) || strings.Contains(err.Error(), "SYNTHETIC") {
			t.Fatal("credential failure lost not-sent evidence", err)
		}
	}
}

func TestSpecCloudVertexStreamCloseCancelsBlockedSDKRead(t *testing.T) {
	ended := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"first\"}]}}]}\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(ended)
	}))
	defer server.Close()
	a := contractApp(t, nil)
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	a.HTTP = &http.Client{Transport: tr}
	defer a.CloseNetwork()
	f := cloudMockFactories(t)
	f.Vertex = func(ctx context.Context, c *CloudSelection, h *http.Client, _ Secrets, _ string) (cloudVertexAPI, error) {
		creds := auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: cloudSyntheticToken{}})
		if err := httptransport.AddAuthorizationMiddleware(h, creds); err != nil {
			return nil, err
		}
		client, err := genai.NewClient(ctx, &genai.ClientConfig{Backend: genai.BackendVertexAI, Project: c.VertexProject, Location: c.VertexLocation, Credentials: creds, HTTPClient: h, HTTPOptions: genai.HTTPOptions{BaseURL: server.URL, APIVersion: "v1", RetryOptions: &genai.HTTPRetryOptions{Attempts: genai.Ptr(int32(1))}}})
		if err != nil {
			return nil, err
		}
		return client.Models, nil
	}
	direct := ""
	response, err := a.cloudUpstreamWith(context.Background(), Source{Provider: "vertex", ProxyURL: &direct}, cloudVertexSelection(), cloudBody(`{"model":"gemini-fixture","input":"x","stream":true}`), "responses", 1<<20, f)
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 8192)
	if n, err := response.Body.Read(buffer); n == 0 || err != nil {
		t.Fatal("first SDK event missing", err)
	}
	reading := make(chan error, 1)
	go func() { _, err := io.Copy(io.Discard, response.Body); reading <- err }()
	closed := make(chan error, 1)
	go func() { closed <- response.Body.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stream close stuck on SDK read")
	}
	select {
	case <-reading:
	case <-time.After(time.Second):
		t.Fatal("reader remained after cancellation")
	}
	select {
	case <-ended:
	case <-time.After(time.Second):
		t.Fatal("provider connection not cancelled")
	}
}
