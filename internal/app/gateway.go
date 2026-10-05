package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

func (a *App) data(w http.ResponseWriter, r *http.Request) {
	if a.webSocketAPI(w, r) {
		return
	}
	if r.Method == "POST" && r.URL.Path == "/v1/responses" {
		var release func()
		var ok bool
		r, release, ok = a.acquireResponsesIngress(w, r)
		if !ok {
			return
		}
		defer release()
	}
	if a.resourceOperationsAPI(w, r) {
		return
	}
	if a.backgroundResponsesEntry(w, r) {
		return
	}
	if a.nativeOperationsAPI(w, r) {
		return
	}
	if r.URL.Path == "/v1/messages" {
		w = &messagesErrors{ResponseWriter: w}
		if r.Method == "POST" {
			if key := r.Header.Get("X-Api-Key"); key != "" {
				if r.Header.Get("Authorization") != "" && bearer(r) != key {
					fail(w, 401, "两种客户端认证头不一致", "")
					return
				}
				r = r.Clone(r.Context())
				r.Header.Set("Authorization", "Bearer "+key)
				r.Header.Del("X-Api-Key")
			}
			if version := r.Header.Get("Anthropic-Version"); version != "" && version != "2023-06-01" {
				fail(w, 400, "仅支持 anthropic-version: 2023-06-01", "anthropic-version")
				return
			}
		}
	}
	if (r.URL.Path == "/v1/responses" || r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/messages") && r.Method == "POST" {
		a.forward(w, r, "")
		return
	}
	if r.URL.Path == "/v1/models" && r.Method == "GET" {
		a.mu.Lock()
		defer a.mu.Unlock()
		key, e := a.Store.keyByDigest(digest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if e != nil || !keyValid(key, time.Now()) {
			fail(w, 401, "客户端 Key 无效", "")
			return
		}
		models := []map[string]any{}
		if key.RouteID == "" {
			src, e := a.Store.source(key.SourceID)
			if e != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			if !src.Deleted && src.Enabled && src.Configured {
				for _, m := range src.Models {
					if allowed(key.ModelAllowlist, m) {
						models = append(models, map[string]any{"id": m, "object": "model", "owned_by": "configured_source"})
					}
				}
			}
		} else {
			rows, e := a.Store.DB.Query("SELECT public_model FROM model_aliases WHERE route_id=? ORDER BY public_model", key.RouteID)
			if e != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			names := []string{}
			for rows.Next() {
				var name string
				if e = rows.Scan(&name); e != nil {
					break
				}
				names = append(names, name)
			}
			if e == nil {
				e = rows.Err()
			}
			rows.Close()
			if e != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
			for _, m := range names {
				if !allowed(key.ModelAllowlist, m) {
					continue
				}
				if _, _, _, e = a.selectSource(key, m, "responses", true); e == nil {
					models = append(models, map[string]any{"id": m, "object": "model", "owned_by": "configured_route"})
				}
			}
		}
		if len(key.ProtocolAllowlist) == 0 && key.ProtocolAllowlist != nil || len(key.OperationAllowlist) == 0 && key.OperationAllowlist != nil {
			models = []map[string]any{}
		}
		writeJSON(w, 200, map[string]any{"object": "list", "data": models})
		return
	}
	if r.URL.Path == "/v1/responses" || r.URL.Path == "/v1/chat/completions" || r.URL.Path == "/v1/messages" || r.URL.Path == "/v1/models" {
		fail(w, 405, "方法不支持", "")
		return
	}
	if strings.HasPrefix(r.URL.Path, "/v1/images/") || strings.HasPrefix(r.URL.Path, "/v1/audio/") || strings.HasPrefix(r.URL.Path, "/v1/files") || r.URL.Path == "/v1/embeddings" || r.URL.Path == "/v1/rerank" || r.URL.Path == "/v1/responses/compact" || r.URL.Path == "/v1/messages/count_tokens" {
		fail(w, 422, "此 operation 尚不可用，请选择已支持的来源和操作", r.URL.Path)
		return
	}
	fail(w, 404, "接口不存在", "")
}
func validateRequest(body map[string]json.RawMessage, src Source) (model, previous string, stream bool, err error) {
	if json.Unmarshal(body["model"], &model) != nil || !slices.Contains(src.Models, model) {
		return "", "", false, errors.New("模型未在此来源配置")
	}
	if v, ok := body["stream"]; ok && json.Unmarshal(v, &stream) != nil {
		return "", "", false, errors.New("stream 必须是布尔值")
	}
	if v, ok := body["previous_response_id"]; ok && string(v) != "null" && json.Unmarshal(v, &previous) != nil {
		return "", "", false, errors.New("previous_response_id 无效")
	}
	var background bool
	if v, ok := body["background"]; ok && (json.Unmarshal(v, &background) != nil || background) {
		return "", "", false, unsupportedFeature("此执行模式不支持 background")
	}
	if v, ok := body["conversation"]; ok && string(v) != "null" {
		return "", "", false, unsupportedFeature("尚不支持 conversation 引用")
	}
	var tools []map[string]json.RawMessage
	if v, ok := body["tools"]; ok && string(v) != "null" {
		if json.Unmarshal(v, &tools) != nil {
			return "", "", false, errors.New("tools 结构无效")
		}
		if err = validateTools(tools); err != nil {
			return
		}
	}
	if v, ok := body["input"]; ok {
		var text string
		if json.Unmarshal(v, &text) != nil {
			var items []map[string]json.RawMessage
			if json.Unmarshal(v, &items) != nil {
				return "", "", false, errors.New("只支持文本与已支持的输入项")
			}
			for _, item := range items {
				var kind string
				_ = json.Unmarshal(item["type"], &kind)
				if kind != "" && kind != "message" && kind != "function_call" && kind != "function_call_output" && kind != "reasoning" && kind != "additional_tools" && kind != "custom_tool_call" && kind != "custom_tool_call_output" && kind != "compaction" && kind != "compaction_trigger" {
					return "", "", false, errors.New("输入项类型未支持")
				}
				if kind == "compaction_trigger" && (src.Kind != "codex_subscription" || !slices.Contains(src.NativeOperations, "compact")) {
					return "", "", false, unsupportedFeature("此来源未声明 Codex 原生流式压缩")
				}
				if kind == "additional_tools" {
					var declarations []map[string]json.RawMessage
					if json.Unmarshal(item["tools"], &declarations) != nil {
						return "", "", false, errors.New("additional_tools 结构无效")
					}
					if e := validateTools(declarations); e != nil {
						return "", "", false, e
					}
				}
				if content, ok := item["content"]; ok {
					var txt string
					if json.Unmarshal(content, &txt) != nil {
						var parts []map[string]json.RawMessage
						if json.Unmarshal(content, &parts) != nil {
							return "", "", false, errors.New("输入内容结构未支持")
						}
						for _, part := range parts {
							var contentKind string
							_ = json.Unmarshal(part["type"], &contentKind)
							if contentKind == "input_file" || contentKind == "input_image" {
								var fileID string
								_ = json.Unmarshal(part["file_id"], &fileID)
								if fileID == "" {
									var imageURL string
									_ = json.Unmarshal(part["image_url"], &imageURL)
									if contentKind != "input_image" || imageURL == "" {
										return "", "", false, unsupportedFeature("此媒体输入需要已归属文件或合法图像URL")
									}
									if e := validateNativeImageURL(imageURL); e != nil {
										return "", "", false, e
									}
								}
							}
							var typ string
							_ = json.Unmarshal(part["type"], &typ)
							if typ != "input_text" && typ != "output_text" && typ != "input_file" && typ != "input_image" {
								var role string
								_ = json.Unmarshal(item["role"], &role)
								if typ == "refusal" && kind == "message" && role == "assistant" {
									continue
								}
								return "", "", false, errors.New("首版仅支持文本模态")
							}
						}
					}
				}
				if kind == "function_call_output" {
					var output string
					if json.Unmarshal(item["output"], &output) != nil {
						return "", "", false, errors.New("首版工具结果需为文本")
					}
				}
			}
		}
	}
	err = validateSourceCapabilities(body, src, previous, stream)
	return
}

// Source restrictions are independent of the client's wire protocol.
func validateSourceCapabilities(body map[string]json.RawMessage, src Source, previous string, stream bool) error {
	if src.Kind == "codex_subscription" {
		if !stream {
			return errors.New("订阅来源要求 stream=true")
		}
		if previous != "" {
			return errors.New("订阅来源要求完整历史，不支持 previous_response_id")
		}
		var store bool
		if v, ok := body["store"]; ok && (json.Unmarshal(v, &store) != nil || store) {
			return errors.New("订阅来源只支持 store=false")
		}
		for _, key := range []string{"temperature", "top_p", "max_output_tokens", "truncation"} {
			if _, ok := body[key]; ok {
				return unsupportedFeature("订阅来源尚未支持参数 " + key)
			}
		}
	}
	return nil
}

func validateTools(tools []map[string]json.RawMessage) error {
	for _, t := range tools {
		var kind string
		_ = json.Unmarshal(t["type"], &kind)
		if kind == "namespace" {
			var nested []map[string]json.RawMessage
			if json.Unmarshal(t["tools"], &nested) != nil {
				return errors.New("工具命名空间结构无效")
			}
			for _, n := range nested {
				var k string
				_ = json.Unmarshal(n["type"], &k)
				if k != "function" && k != "custom" {
					return errors.New("命名空间只支持客户端 function/custom 工具")
				}
			}
		} else if kind != "function" && kind != "custom" && kind != "web_search" {
			return errors.New("首版只支持客户端 function/custom 工具")
		}
	}
	return nil
}

// Prepare the one upstream POST; converters never acquire credentials or dispatch.
func prepareUpstream(ctx context.Context, src Source, body map[string]json.RawMessage, raw []byte, secret, account string, stream bool, c CodexConfig) (*http.Request, error) {
	if src.Kind == "codex_subscription" {
		body["store"] = json.RawMessage("false")
		if _, ok := body["instructions"]; !ok {
			body["instructions"] = json.RawMessage(`""`)
		}
		raw = []byte(encode(body))
	}
	path := "/responses"
	if src.NativeProtocol == "chat_completions" {
		path = "/chat/completions"
	}
	if src.NativeProtocol == "messages" {
		path = "/messages"
	}
	if src.NativeProtocol == "gemini" {
		var model string
		_ = json.Unmarshal(body["model"], &model)
		path = "/models/" + url.PathEscape(model) + ":generateContent"
		if stream {
			path = "/models/" + url.PathEscape(model) + ":streamGenerateContent?alt=sse"
		}
		var wire map[string]json.RawMessage
		if json.Unmarshal(raw, &wire) != nil {
			return nil, errors.New("Gemini请求无效")
		}
		delete(wire, "model")
		delete(wire, "stream")
		raw = []byte(encode(wire))
	}
	address := safeEndpoint(src.BaseURL, path)
	if src.NativeProtocol == "gemini" {
		address = safeEndpoint(strings.TrimSuffix(strings.TrimRight(src.BaseURL, "/"), "/v1beta"), "/v1beta"+path)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", address, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.GetBody = nil // Model POSTs are not replayable by the transport.
	if src.Kind != "none" {
		req.Header.Set("Authorization", "Bearer "+secret)
	}
	if src.Provider == "azure" {
		req.Header.Del("Authorization")
		req.Header.Set("Api-Key", secret)
	}
	if src.NativeProtocol == "messages" {
		req.Header.Del("Authorization")
		req.Header.Set("X-Api-Key", secret)
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	if src.NativeProtocol == "gemini" {
		req.Header.Del("Authorization")
		req.Header.Set("X-Goog-Api-Key", secret)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if stream {
		req.Header.Set("Accept", "text/event-stream")
	}
	if src.Kind == "codex_subscription" {
		if chatGPTDirectSource(src) {
			req.Header.Set("User-Agent", "Cove/"+Version)
		} else {
			req.Header.Set("Chatgpt-Account-Id", account)
			req.Header.Set("originator", "codex_cli_rs")
			req.Header.Set("Version", c.ClientVersion)
			req.Header.Set("User-Agent", "gatt/"+Version+" codex_cli_rs/"+c.ClientVersion)
		}
	}
	return req, nil
}

func sourceStream(src Source, resp *http.Response) (string, io.Reader) {
	contentType := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	var body io.Reader = resp.Body
	// Codex may omit Content-Type. Peek without consuming any native SSE bytes.
	if src.Kind == "codex_subscription" && contentType == "" {
		reader := bufio.NewReader(resp.Body)
		prefix, _ := reader.Peek(6)
		body = reader
		if bytes.HasPrefix(prefix, []byte("event:")) || bytes.HasPrefix(prefix, []byte("data:")) || bytes.HasPrefix(prefix, []byte(":")) {
			mediaType = "text/event-stream"
		}
	}
	return mediaType, body
}

func (a *App) forward(w http.ResponseWriter, r *http.Request, adminSource string) (rec *Record) {
	protocol := "responses"
	if r.URL.Path == "/v1/chat/completions" {
		protocol = "chat_completions"
	}
	if r.URL.Path == "/v1/messages" {
		protocol = "messages"
	}
	if strings.HasPrefix(r.URL.Path, "/v1beta/models/") {
		protocol = "gemini"
	}
	var adapter *compatOutput
	var geminiBinding *GeminiCallBinding
	var geminiDirect *geminiObserver
	var geminiOutput *ResponsesToGeminiStream
	var geminiServerTools bool
	var nativeDirect bool
	var ingressKeyID string
	requestID := id("req")
	if lease := dataIngress(r); lease != nil {
		requestID = lease.RequestID
	}
	w.Header().Set("X-Gateway-Request-Id", requestID)
	// Authenticate and reserve capacity before buffering any request bytes.
	a.mu.Lock()
	cfg := a.Config
	configVersion := 1
	if settings, e := a.Store.readRuntimeSettings(); e == nil {
		configVersion = settings.Version
	}
	if lease := dataIngress(r); lease != nil {
		cfg = lease.Config
		configVersion = lease.ConfigVersion
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(cfg.TotalTimeout)*time.Second)
	defer cancel()
	var refreshTiming credentialRefreshTiming
	ctx = context.WithValue(ctx, credentialRefreshTimingKey{}, &refreshTiming)
	var ingressKey ClientKey
	var queuedFor time.Duration
	if lease := dataIngress(r); lease != nil {
		queuedFor = lease.QueuedFor
	}
	if a.stopping || a.storageFailed.Load() {
		a.mu.Unlock()
		fail(w, 503, "服务正在关闭或存储异常，请检查本机状态", "")
		return
	}
	if adminSource == "" {
		key, err := a.Store.keyByDigest(digest(bearer(r)))
		if err != nil || !keyValid(key, time.Now()) {
			a.mu.Unlock()
			fail(w, 401, "客户端 Key 无效或已撤销", "")
			return
		}
		if !allowed(key.ProtocolAllowlist, protocol) || !allowed(key.OperationAllowlist, "generate") && !allowed(key.OperationAllowlist, "compact") {
			a.mu.Unlock()
			fail(w, 403, "Key 无此协议或操作权限", "")
			return
		}
		ingressKey = key
		ingressKeyID = key.ID
	} else if a.sourceRunning(adminSource) {
		a.mu.Unlock()
		fail(w, 409, "该来源已有运行请求，请等待完成后再测试", "")
		return
	}
	if dataIngress(r) == nil {
		if len(a.slots) >= a.Config.MaxConcurrent || ingressKey.Limits.MaxConcurrent != nil && a.keyActive[ingressKey.ID] >= *ingressKey.Limits.MaxConcurrent {
			if ok, wait := a.waitCapacity(w, ctx, ingressKey, nil, requestID, "ingress"); !ok {
				a.mu.Unlock()
				return
			} else {
				queuedFor += wait
			}
		}
		select {
		case a.slots <- struct{}{}:
			if ingressKeyID != "" {
				a.keyActive[ingressKeyID]++
			}
			a.mu.Unlock()
		default:
			a.mu.Unlock()
			fail(w, 429, "本机并发容量已满，请稍后重试", "")
			return
		}
		defer func() {
			a.mu.Lock()
			<-a.slots
			if ingressKeyID != "" {
				a.keyActive[ingressKeyID]--
			}
			a.signalAdmission()
			a.mu.Unlock()
		}()
	} else {
		a.mu.Unlock()
	}
	controller := http.NewResponseController(w)
	readUntil := time.Now().Add(time.Duration(cfg.HeaderTimeout) * time.Second)
	if deadline, ok := ctx.Deadline(); ok && deadline.Before(readUntil) {
		readUntil = deadline
	}
	_ = controller.SetReadDeadline(readUntil)
	readCancellationDone := make(chan struct{})
	stopReadCancellation := context.AfterFunc(ctx, func() {
		defer close(readCancellationDone)
		_ = controller.SetReadDeadline(time.Now())
	})
	r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxBody)
	raw, e := io.ReadAll(r.Body)
	if !stopReadCancellation() {
		<-readCancellationDone
	}
	_ = controller.SetReadDeadline(time.Time{})
	if e != nil {
		var max *http.MaxBytesError
		var timeout net.Error
		if errors.As(e, &max) {
			fail(w, 413, "请求体超出本机限制", "")
		} else if errors.As(e, &timeout) && timeout.Timeout() || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fail(w, 408, "请求体读取超时，请重新发送", "")
		} else {
			fail(w, 400, "请求体读取失败", "")
		}
		return
	}
	var body map[string]json.RawMessage
	if protocol == "responses" && (json.Unmarshal(raw, &body) != nil || body == nil) {
		fail(w, 400, "请求必须为 JSON 对象", "")
		return
	}
	a.mu.Lock()
	reject := func(status int, message, param string) { a.mu.Unlock(); fail(w, status, message, param) }
	if a.stopping || a.storageFailed.Load() {
		reject(503, "服务正在关闭或存储异常，请检查本机状态", "")
		return
	}
	if adminSource != "" && a.sourceRunning(adminSource) {
		reject(409, "该来源已有运行请求，请等待完成后再测试", "")
		return
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(raw, &envelope) != nil || envelope == nil {
		reject(400, "请求必须为 JSON 对象", "")
		return
	}
	if protocol == "gemini" {
		endpoint, pe := parseGeminiEndpoint(r)
		if pe != nil {
			reject(400, pe.Error(), "")
			return
		}
		envelope, pe = geminiBody(raw, endpoint)
		if pe != nil {
			reject(400, pe.Error(), "")
			return
		}
		envelope["model"] = json.RawMessage(encode(endpoint.Model))
		envelope["stream"] = json.RawMessage(encode(endpoint.Stream))
		raw = []byte(encode(envelope))
	}
	var publicModel string
	if json.Unmarshal(envelope["model"], &publicModel) != nil || publicModel == "" {
		reject(400, "model 必须为非空字符串", "model")
		return
	}
	clientRaw := append([]byte(nil), raw...)
	var key ClientKey
	var src Source
	var sentModel string
	var selectionReasons []CandidateReason
	var policySnapshot RoutingPolicySnapshot
	var policyInput RoutingPolicyInput
	var selectedCandidate RoutingPolicyCandidate
	var upstreamRetryAfter string
	if adminSource != "" {
		src, e = a.Store.source(adminSource)
		key = ClientKey{ID: "admin_test_" + adminSource, Name: "管理端测试", SourceID: adminSource}
	} else {
		key, e = a.Store.keyByDigest(digest(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))
		if e != nil || !keyValid(key, time.Now()) {
			reject(401, "客户端 Key 无效或已撤销", "")
			return
		}
		if !allowed(key.ProtocolAllowlist, protocol) || !allowed(key.OperationAllowlist, "generate") && !allowed(key.OperationAllowlist, "compact") || !allowed(key.ModelAllowlist, publicModel) {
			reject(403, "Key 无此模型、协议或操作权限", "")
			return
		}
		if key.RouteID == "" {
			src, e = a.Store.source(key.SourceID)
			sentModel = publicModel
		} else {
			policyInput = RoutingPolicyInput{Key: key, PublicModel: publicModel, Protocol: protocol, Operation: "generate", ClientRaw: clientRaw, SessionID: r.Header.Get("X-Cove-Session-Id"), MaxResponse: cfg.MaxResponse, SnapshotOut: &policySnapshot, Eligibility: func(s Source, b map[string]json.RawMessage) error {
				_, err := a.prepareAccounting(key, s, b)
				return err
			}}
			if len(policyInput.SessionID) > 200 {
				reject(400, "Session ID 过长", "X-Cove-Session-Id")
				return
			}
			var boundPrevious string
			_ = json.Unmarshal(envelope["previous_response_id"], &boundPrevious)
			if boundPrevious != "" {
				var boundSource, boundModel string
				bindErr := a.Store.DB.QueryRow("SELECT source_id,model FROM bindings WHERE response_id=? AND key_id=? ORDER BY rowid DESC LIMIT 1", boundPrevious, key.ID).Scan(&boundSource, &boundModel)
				if bindErr != nil {
					reject(409, "前序响应不属于此 Key 或绑定已失效，请建立新会话", "previous_response_id")
					return
				}
				policyInput.AuthoritativeBinding = true
				policyInput.BoundSourceID, policyInput.BoundModel = boundSource, boundModel
			}
			if protocol == "responses" {
				fileSource, fe := a.fileInputSource(envelope["input"], key)
				if fe != nil {
					var re *resourceError
					status := 400
					if errors.As(fe, &re) {
						status = re.Code
					}
					reject(status, fe.Error(), "input")
					return
				}
				if fileSource != "" {
					if policyInput.BoundSourceID != "" && policyInput.BoundSourceID != fileSource {
						reject(409, "文件与前序响应属于不同来源", "input")
						return
					}
					policyInput.AuthoritativeBinding = true
					policyInput.BoundSourceID = fileSource
				}
			}
			src, sentModel, selectionReasons, e = a.selectSourceExcluding(key, publicModel, protocol, false, nil, policyInput)
			if e != nil {
				route, re := a.Store.route(key.RouteID)
				if re == nil && route.QueueLimit > 0 {
					queuedPlan := policyInput
					queuedPlan.IncludeBusySources = true
					waiting, waitingModel, reasons, we := a.selectSourceExcluding(key, publicModel, protocol, false, nil, queuedPlan)
					if we == nil && waiting.MaxConcurrent != nil && a.accountActive[waiting.AccountID] >= *waiting.MaxConcurrent {
						if ok, wait := a.waitCapacity(w, ctx, key, &waiting, requestID, "account"); !ok {
							a.mu.Unlock()
							return
						} else {
							queuedFor += wait
						}
						src, sentModel, selectionReasons, e = waiting, waitingModel, reasons, nil
					}
				}
			}
			if e != nil {
				status := 429
				var selection *selectionError
				if errors.As(e, &selection) {
					status = selection.Status
				}
				reject(status, e.Error(), "target")
				return
			}
		}
	}
	if e != nil || src.Deleted || !src.Enabled {
		reject(503, "来源不存在或已停用", "")
		return
	}
	if sentModel == "" {
		sentModel = publicModel
	}
	if sentModel != publicModel {
		envelope["model"] = json.RawMessage(encode(sentModel))
		raw = []byte(encode(envelope))
	}
	if protocol == "responses" {
		body = envelope
		originalInput := append([]byte(nil), body["input"]...)
		if err := a.mapOwnedFileInputs(body, key, src); err != nil {
			var resource *resourceError
			status := 400
			if errors.As(err, &resource) {
				status = resource.Code
			}
			reject(status, err.Error(), "input")
			return
		}
		if !bytes.Equal(originalInput, body["input"]) {
			raw = []byte(encode(body))
		}
	}
	body, adapter, nativeDirect, raw, previous, stream, err := candidateInput(raw, protocol, src, sentModel, cfg.MaxResponse)
	model := sentModel
	if err != nil {
		code := requestErrorStatus(err)
		field := ""
		var ge *GeminiConversionError
		if errors.As(err, &ge) {
			code, field = geminiConversionStatus(err)
		}
		reject(code, err.Error(), field)
		return
	}
	operation := "generate"
	compactTrigger := false
	var requestItems []map[string]json.RawMessage
	_ = json.Unmarshal(body["input"], &requestItems)
	for i, item := range requestItems {
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		if kind == "compaction_trigger" {
			if compactTrigger || i != len(requestItems)-1 || len(item) != 1 || protocol != "responses" || src.Kind != "codex_subscription" || !stream {
				reject(422, "Codex compaction_trigger 必须是原生流式历史的最后一项", "input")
				return
			}
			compactTrigger = true
			operation = "compact"
		}
	}
	if adminSource == "" && !allowed(key.OperationAllowlist, operation) {
		reject(403, "Key 无此操作权限", "operation")
		return
	}
	if src.Kind == "codex_subscription" {
		if e := a.validateNativeOpaqueHistory(body, key, src, model); e != nil {
			reject(409, e.Error(), "input")
			return
		}
	}
	if src.NativeProtocol == "gemini" && !nativeDirect {
		geminiBinding = &GeminiCallBinding{Store: a.Store, Key: key, Source: src, Model: sentModel}
		if err = geminiBinding.Verify(body); err != nil {
			code, field := geminiConversionStatus(err)
			reject(code, err.Error(), field)
			return
		}
	}
	if src.Kind == "api_key" && protocol == "responses" && src.NativeProtocol == "responses" {
		if opaqueErr := a.validateNativeOpaqueHistory(body, key, src, model); opaqueErr != nil {
			reject(409, opaqueErr.Error(), "input")
			return
		}
	}
	if cachedResponseID(previous) {
		reject(409, "缓存响应不能用于 previous_response_id，请发送完整历史", "previous_response_id")
		return
	}
	if previous != "" && ((!src.Continuation && adminSource == "") || !a.Store.continuation(previous, key, src, model)) {
		reject(409, "续接未验证或响应不属于当前 Key、来源、凭据代次与模型；请建立新会话", "previous_response_id")
		return
	}
	if src.MaxConcurrent != nil && a.accountActive[src.AccountID] >= *src.MaxConcurrent {
		if ok, wait := a.waitCapacity(w, ctx, key, &src, requestID, "account"); !ok {
			a.mu.Unlock()
			return
		} else {
			queuedFor += wait
		}
	}
	if key.RouteID != "" {
		route, routeErr := a.Store.route(key.RouteID)
		if routeErr != nil {
			reject(503, storageError().Error(), "")
			return
		}
		if route.MaxConcurrent != nil && a.routeActive[route.ID] >= *route.MaxConcurrent {
			if ok, wait := a.waitCapacity(w, ctx, key, &src, requestID, "route"); !ok {
				a.mu.Unlock()
				return
			} else {
				queuedFor += wait
			}
		}
	}
	secret := ""
	account := ""
	if blocked, reason := quotaDispatchBlocked(src, model, operation, time.Now()); blocked {
		reject(429, reason, "quota")
		return
	}
	if src.Kind == "codex_subscription" {
		var c Credential
		c, err = a.subscriptionCredential(ctx, &src)
		secret = c.Access
		account = c.Account
	} else if src.Kind == "none" || cloudProvider(src) {
		secret = ""
	} else if !src.Configured {
		err = errors.New("来源尚未配置凭据")
	} else {
		secret, err = a.Secrets.Get(src.CredentialRef)
	}
	if err != nil {
		reject(503, err.Error(), "")
		return
	}
	// A shared credential refresh can release mu; revalidate before publishing admission.
	if adminSource == "" {
		currentKey, keyErr := a.Store.keyByDigest(digest(bearer(r)))
		if keyErr != nil || !keyValid(currentKey, time.Now()) || currentKey.Version != key.Version {
			reject(409, "Key 已修改，请重新发送请求", "")
			return
		}
	}
	if src.MaxConcurrent != nil && a.accountActive[src.AccountID] >= *src.MaxConcurrent {
		reject(429, "账号并发已满", "")
		return
	}
	selectedCandidate, err = a.candidateFor(src, model)
	if err != nil {
		var selection *selectionError
		if errors.As(err, &selection) {
			reject(selection.Status, selection.Message, "model")
			return
		}
		reject(503, storageError().Error(), "")
		return
	}
	src = selectedCandidate.Source
	if protocol == "messages" {
		if betaErr := validateMessagesBeta(r.Header.Get("Anthropic-Beta"), nativeDirect, selectedCandidate.Model, src, adapter); betaErr != nil {
			reject(422, betaErr.Error(), "anthropic-beta")
			return
		}
	}
	if blocked, reason := quotaDispatchBlocked(src, model, operation, time.Now()); blocked {
		reject(429, reason, "quota")
		return
	}
	if protocol == "responses" {
		var items []struct{ Content []map[string]json.RawMessage }
		_ = json.Unmarshal(body["input"], &items)
		for _, item := range items {
			for _, part := range item.Content {
				var kind string
				_ = json.Unmarshal(part["type"], &kind)
				if kind == "input_image" && (src.Kind != "api_key" || src.NativeProtocol != "responses" || !slices.Contains(selectedCandidate.Model.Modalities, "image")) {
					reject(422, "所选模型尚未声明原生图像输入能力", "input")
					return
				}
			}
		}
	}
	if nativeDirect && protocol == "gemini" {
		if !(src.Kind == "api_key" && src.Provider == "gemini" || cloudProvider(src) && src.Provider == "vertex") || !slices.Contains(src.NativeOperations, "generate") {
			reject(422, "来源未声明原生 Gemini generate", "")
			return
		}
		parts, pe := geminiParts(body)
		if pe != nil {
			status := requestErrorStatus(pe)
			var selection *selectionError
			if errors.As(pe, &selection) {
				status = selection.Status
			}
			reject(status, pe.Error(), "")
			return
		}
		if pe = a.Store.verifyGeminiHistory(parts, key, src, selectedCandidate.Model); pe != nil {
			reject(409, pe.Error(), "contents")
			return
		}
		if geminiServerTools, pe = validateGeminiTools(body, src, selectedCandidate.Model); pe != nil {
			reject(422, pe.Error(), "tools")
			return
		}
	}
	if !nativeDirect || protocol != "gemini" {
		err = validateExtendedServerTools(body, src, selectedCandidate.Model)
	}
	if err != nil {
		reject(422, err.Error(), "tools")
		return
	}
	cloudBody := body
	if cloudProvider(src) {
		if src.NativeProtocol == "gemini" {
			if json.Unmarshal(raw, &cloudBody) != nil {
				reject(400, "云原生请求 JSON 无效", "")
				return
			}
		}
		if err = validateCloudRequest(src, cloudBody, src.NativeProtocol); err != nil {
			a.mu.Unlock()
			accountingFailure(w, err)
			return
		}
	}
	accountingPlan, accountingErr := a.prepareAccounting(key, src, body)
	if accountingErr != nil {
		a.mu.Unlock()
		accountingFailure(w, accountingErr)
		return
	}
	strictCount := accountingPlan != nil && accountingPlan.StrictInputCountRequired
	if accountingErr = a.completeStrictAccounting(ctx, key, src, body, secret, requestID, adminSource != "", accountingPlan); accountingErr != nil {
		a.mu.Unlock()
		accountingFailure(w, accountingErr)
		return
	}
	var tokenEstimate int64
	var estimateKind string
	var tokenErr error
	if strictCount {
		tokenEstimate = accountingPlan.EstimatedInputTokens + accountingPlan.EstimatedOutputTokens
		estimateKind = "provider_input_count_plus_output_cap"
	} else {
		tokenEstimate, estimateKind, tokenErr = reservationEstimate(body, raw)
	}
	if tokenErr != nil && key.Limits.TPM != nil {
		reject(422, tokenErr.Error(), "limits.tpm")
		return
	}
	if ok, retry := a.checkTPM(key, tokenEstimate, time.Now()); !ok {
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		reject(429, "此 Key 的60秒 token 窗口达到上限（本地预留）", "limits.tpm")
		return
	}
	if ok, retry := a.consumeRPM(key, time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		reject(429, "此 Key 请求速率达到上限", "limits.rpm")
		return
	}
	if key.RouteID != "" && !a.policyState.ClaimProbe(selectedCandidate, time.Now()) {
		a.refundRPM(key)
		reject(429, "此来源仍在冷却或恢复探测已占用", "")
		return
	}
	rec = &Record{ID: requestID, Origin: "client", KeyID: key.ID, ClientName: key.Name, Fingerprint: key.Fingerprint, SourceID: src.ID, SourceName: src.Name, Generation: src.Generation, Model: publicModel, SentModel: model, Status: "dispatching", UpstreamStatus: "unknown", DeliveryStatus: "not_started", ObservationStatus: "complete", Started: time.Now().UTC(), Completeness: "unknown", Price: src.Price, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, RouteID: key.RouteID, AttemptID: id("att"), Sequence: 1, SelectionReasons: selectionReasons}
	if nativeDirect && protocol == "gemini" {
		geminiDirect = &geminiObserver{store: a.Store, key: key, source: src, model: selectedCandidate.Model, record: rec, seen: map[int]bool{}, terminal: map[int]bool{}}
	}
	if protocol == "gemini" && !nativeDirect {
		geminiOutput = newResponsesToGeminiStream(cfg.MaxResponse)
	}
	rec.ModelID = selectedCandidate.Model.ID
	if key.RouteID != "" {
		rec.RoutingPolicy = &policySnapshot
	}
	rec.QueueMS = queuedFor.Milliseconds()
	rec.SourceVersion = src.Version
	rec.KeyVersion = key.Version
	rec.ConfigVersion = configVersion
	if route, e := a.Store.route(key.RouteID); e == nil {
		rec.RouteVersion = route.Version
	}
	rec.Accounting = accountingPlan
	rec.Operation = operation
	rec.RequestBytes = int64(len(clientRaw))
	rec.TraceParent = r.Header.Get("Traceparent")
	rec.RefreshTiming = refreshTiming
	rec.Protocol = protocol
	rec.Version = 1
	rec.Submission = "possible"
	rec.AttemptStarted = rec.Started
	rec.ReservedTokens = tokenEstimate
	rec.TokenReservationSource = estimateKind
	if adapter != nil {
		rec.Adjustments = adapter.adjustments
		if len(rec.Adjustments) > 0 {
			w.Header().Set("X-Cove-Compatibility", strings.Join(rec.Adjustments, ","))
		}
		if adapter.wireInput != nil && adapter.stream {
			rec.WireUsageSource = "estimated"
			w.Header().Set("X-Cove-Usage-Source", "estimated;o200k_base;tokenizer-0.8.1")
		}
	}
	if adminSource != "" {
		rec.Origin = "admin_test"
		if r.Header.Get("X-Cove-Verification") == "model" {
			rec.Origin = "verification"
		}
	}
	if err = a.Store.record(*rec); err != nil {
		a.refundRPM(key)
		a.policyState.ReleaseProbe(selectedCandidate)
		var budgetErr *accountingError
		if errors.As(err, &budgetErr) {
			a.mu.Unlock()
			accountingFailure(w, err)
			return nil
		}
		a.markStorageFailure()
		reject(503, storageError().Error(), "")
		return nil
	}
	a.reserveTPM(key, rec.ID, tokenEstimate, rec.Started)
	a.running[rec.ID] = cancel
	a.runningSources[rec.ID] = src.ID
	a.runningKeys[rec.ID] = key.ID
	a.accountActive[src.AccountID]++
	if key.RouteID != "" {
		a.routeActive[key.RouteID]++
	}
	if adminSource == "" {
		now := time.Now().UTC()
		key.LastSeen = &now
		if _, err = a.Store.DB.Exec("UPDATE client_keys SET data=? WHERE id=?", encode(key), key.ID); err != nil {
			a.markStorageFailure()
		}
	}
	a.mu.Unlock()
	var cache *ResponseCacheDescriptor
	if geminiServerTools || hasExtendedServerTools(body) {
		rec.ToolCostStatus = "unknown"
	}
	started := time.Now()
	committed := false
	observationSkipped := false
	var idleExpired atomic.Bool
	defer func() {
		now := time.Now().UTC()
		rec.Ended = &now
		rec.DurationMS = time.Since(started).Milliseconds()
		if rec.Status == "dispatching" || rec.Status == "streaming" {
			rec.Status = "failed"
			rec.ErrorStage = "stream"
			rec.ErrorSummary = "未收到正式完成响应，上游执行与最终费用可能未知"
		}
		if ctx.Err() != nil && rec.Status != "succeeded" {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || idleExpired.Load() {
				rec.Status = "failed"
				rec.ErrorStage = "timeout"
			} else {
				rec.Status = "cancelled"
				rec.ErrorStage = "cancelled"
			}
			rec.ErrorSummary = "本地传输已结束，上游执行与最终费用可能未知"
		}
		// Interim usage (including Messages' initial output_tokens=0) is
		// observable, but cannot settle the full cost without a provider terminal.
		if rec.UpstreamStatus == "unknown" {
			rec.ObservationStatus = "partial"
			observationSkipped = true
		}
		rec.Completeness = usageCompleteness(rec.Usage)
		if observationSkipped && rec.Completeness == "complete" {
			rec.Completeness = "partial"
		}
		if rec.Submission == "not_sent" {
			zero := "0"
			rec.Cost = &zero
		} else if src.Kind != "codex_subscription" {
			if rec.Completeness == "complete" {
				rec.Cost = estimate(rec.Usage, rec.Price)
			}
			if rec.Cost == nil {
				rec.PartialCost = estimatePartial(rec.Usage, rec.Price)
			}
		}
		finalizeExtendedCost(rec)
		if err := a.Store.record(*rec); err != nil {
			a.markStorageFailure()
		} else {
			a.observeExecution(*rec, src, true)
		}
		if rec.CacheHit {
			a.policyState.ReleaseProbe(selectedCandidate)
		} else {
			a.policyState.ObserveAttempt(selectedCandidate, *rec, upstreamRetryAfter, "", false, now)
		}
		a.mu.Lock()
		a.settleTPM(key, *rec)
		delete(a.running, rec.ID)
		delete(a.runningSources, rec.ID)
		delete(a.runningKeys, rec.ID)
		a.signalAdmission()
		a.accountActive[src.AccountID]--
		if key.RouteID != "" {
			a.routeActive[key.RouteID]--
		}
		current, e := a.Store.source(src.ID)
		if e == nil && current.Generation == src.Generation && current.AccountGeneration == src.AccountGeneration && current.Version == src.Version {
			if rec.HTTPStatus == 401 {
				if current.Kind == "codex_subscription" {
					current.AuthStatus = "needs_reauth"
				} else {
					current.AuthStatus = "rejected"
				}
			}
			if rec.Origin == "admin_test" {
				current.Verification.TestedAt = &now
				current.Verification.Model = rec.Model
				current.Verification.RequestID = rec.ID
				current.Verification.Capabilities = []string{}
				if rec.Status == "succeeded" {
					current.Verification.Status = "passed"
					if stream {
						current.Verification.Capabilities = []string{"text_sse"}
					} else {
						current.Verification.Capabilities = []string{"text_json"}
					}
				} else {
					current.Verification.Status = "failed"
				}
			}
			lastStart, _ := current.Quota["call_started_at"].(string)
			last, _ := time.Parse(time.RFC3339Nano, lastStart)
			if !rec.Started.Before(last) {
				current.Quota["call_health"] = rec.Status
				current.Quota["call_observed_at"] = now
				current.Quota["call_started_at"] = rec.Started
			}
			if e = a.Store.saveSource(current); e != nil {
				a.markStorageFailure()
			}
		}
		a.mu.Unlock()
	}()
	req, err := prepareUpstream(ctx, src, body, raw, secret, account, stream, cfg.Codex)
	if err != nil {
		rec.Status = "failed"
		rec.ErrorStage = "prepare"
		rec.Submission = "not_sent"
		fail(w, 502, "上游地址无效", "")
		return
	}
	if protocol == "messages" && nativeDirect && r.Header.Get("Anthropic-Beta") != "" {
		req.Header.Set("Anthropic-Beta", r.Header.Get("Anthropic-Beta"))
	}
	if !stream {
		a.mu.Lock()
		cacheRaw := raw
		if src.NativeProtocol == "gemini" {
			var wire map[string]json.RawMessage
			_ = json.Unmarshal(raw, &wire)
			delete(wire, "model")
			delete(wire, "stream")
			cacheRaw = []byte(encode(wire))
		}
		cache, err = a.responseCacheDescriptor(key, src, selectedCandidate.Model, *rec, cacheRaw, req.Header)
		var entry *ResponseCacheEntry
		if err == nil && cache != nil {
			entry, err = a.lookupResponseCache(cache)
		}
		a.mu.Unlock()
		if err != nil {
			rec.Status = "failed"
			rec.ErrorStage = "response_cache"
			fail(w, 503, storageError().Error(), "")
			return
		}
		if entry != nil {
			if err = replayResponseCache(w, entry, protocol, rec); err != nil {
				rec.Status = "failed"
				rec.ErrorStage = "downstream_write"
			}
			return
		}
		if cache != nil {
			w.Header().Set("X-Cove-Cache", "miss")
		} else {
			w.Header().Set("X-Cove-Cache", "bypass")
		}
	}
	var maxAttempts = 1
	var initialRouteVersion int
	if key.RouteID != "" {
		route, e := a.Store.route(key.RouteID)
		if e == nil {
			maxAttempts = route.MaxAttempts
			initialRouteVersion = route.Version
		}
	}
	excludedSources := map[string]bool{}
	var resp *http.Response
	for {
		if cloudProvider(src) {
			resp, err = a.cloudUpstream(ctx, src, cloudBody, src.NativeProtocol, cfg.MaxResponse)
		} else {
			resp, err = a.doUpstream(req, src)
		}
		if err == nil {
			break
		}
		var dial *net.OpError
		var cloudNotSent *cloudBeforeSendError
		if errors.As(err, &cloudNotSent) {
			rec.Submission = "not_sent"
			rec.UpstreamStatus = "not_submitted"
		}
		if errors.As(err, &dial) && dial.Op == "dial" {
			rec.Submission = "not_sent"
			rec.UpstreamStatus = "not_submitted"
		}
		if key.RouteID == "" || rec.Sequence >= maxAttempts || ctx.Err() != nil || !errors.As(err, &dial) || dial.Op != "dial" {
			break
		}
		excludedSources[src.ID] = true
		a.mu.Lock()
		currentKey, keyErr := a.Store.keyByDigest(digest(bearer(r)))
		route, routeErr := a.Store.route(key.RouteID)
		if keyErr != nil || !keyValid(currentKey, time.Now()) || currentKey.Version != key.Version || routeErr != nil || route.Version != initialRouteVersion {
			a.mu.Unlock()
			break
		}
		var nextPolicySnapshot RoutingPolicySnapshot
		nextPolicyInput := policyInput
		nextPolicyInput.OwnedRequestID = rec.ID
		nextPolicyInput.SnapshotOut = &nextPolicySnapshot
		candidate, sent, reasons, selectErr := a.selectSourceExcluding(key, publicModel, protocol, false, excludedSources, nextPolicyInput)
		if selectErr != nil {
			a.mu.Unlock()
			break
		}
		nextBody, nextAdapter, nextNative, nextRaw, nextPrevious, nextStream, prepareErr := candidateInput(clientRaw, protocol, candidate, sent, cfg.MaxResponse)
		if prepareErr != nil || nextPrevious != "" {
			a.mu.Unlock()
			break
		}
		nextCloudBody := nextBody
		if cloudProvider(candidate) {
			if candidate.NativeProtocol == "gemini" {
				prepareErr = json.Unmarshal(nextRaw, &nextCloudBody)
			}
			if prepareErr == nil {
				prepareErr = validateCloudRequest(candidate, nextCloudBody, candidate.NativeProtocol)
			}
			if prepareErr != nil {
				a.mu.Unlock()
				break
			}
		}
		var nextSecret, nextAccount string
		refreshTiming = credentialRefreshTiming{}
		if candidate.Kind == "codex_subscription" {
			var credential Credential
			credential, prepareErr = a.subscriptionCredential(ctx, &candidate)
			nextSecret, nextAccount = credential.Access, credential.Account
		} else if candidate.Kind == "none" || cloudProvider(candidate) {
			nextSecret = ""
		} else {
			nextSecret, prepareErr = a.Secrets.Get(candidate.CredentialRef)
		}
		if prepareErr != nil {
			a.mu.Unlock()
			break
		}
		now := time.Now().UTC()
		rec.Status = "failed"
		rec.UpstreamStatus = "not_submitted"
		rec.Submission = "not_sent"
		rec.ErrorStage = "connect"
		rec.ErrorSummary = "连接建立失败，确认本次请求未发送"
		rec.Ended = &now
		rec.DurationMS = now.Sub(rec.AttemptStarted).Milliseconds()
		zero := "0.000000000000"
		rec.Cost = &zero
		if storeErr := a.Store.record(*rec); storeErr != nil {
			a.markStorageFailure()
			a.mu.Unlock()
			err = storeErr
			break
		}
		a.observeExecution(*rec, src, false)
		a.policyState.ObserveAttempt(selectedCandidate, *rec, "", "", false, now)
		nextCandidate, candidateErr := a.candidateFor(candidate, sent)
		if candidateErr != nil {
			a.mu.Unlock()
			err = candidateErr
			break
		}
		if protocol == "messages" {
			if betaErr := validateMessagesBeta(r.Header.Get("Anthropic-Beta"), nextNative, nextCandidate.Model, candidate, nextAdapter); betaErr != nil {
				a.mu.Unlock()
				err = &accountingError{422, "anthropic-beta", betaErr.Error()}
				break
			}
		}
		a.settleTPM(key, *rec)
		var nextTokens int64
		var nextEstimate string
		if !a.policyState.ClaimProbe(nextCandidate, now) {
			a.mu.Unlock()
			break
		}
		next := *rec
		next.RefreshTiming = refreshTiming
		next.ModelID = nextCandidate.Model.ID
		next.RoutingPolicy = &nextPolicySnapshot
		next.FirstContentAt = nil
		next.ReservedTokens = nextTokens
		next.TokenReservationSource = nextEstimate
		next.Sequence++
		next.AttemptID = id("att")
		next.AttemptStarted = now
		next.SourceID = candidate.ID
		next.SourceName = candidate.Name
		next.SourceVersion = candidate.Version
		next.ResponseBytes = 0
		next.UpstreamBytes = 0
		next.FirstEvent = nil
		next.Usage = Usage{}
		next.Completeness = "unknown"
		next.PartialCost = nil
		next.UpstreamRequestID = ""
		next.Generation = candidate.Generation
		next.AccountID = candidate.AccountID
		next.AccountGeneration = candidate.AccountGeneration
		next.SentModel = sent
		next.SelectionReasons = reasons
		next.Status = "dispatching"
		next.UpstreamStatus = "unknown"
		next.Submission = "possible"
		next.ErrorStage = ""
		next.ErrorSummary = ""
		next.Ended = nil
		next.Cost = nil
		next.DurationMS = 0
		next.Price = candidate.Price
		next.Adjustments = nil
		if nextAdapter != nil {
			next.Adjustments = nextAdapter.adjustments
		}
		next.Accounting, prepareErr = a.prepareAccounting(key, candidate, nextBody)
		if prepareErr != nil {
			a.policyState.ReleaseProbe(nextCandidate)
			a.mu.Unlock()
			err = prepareErr
			break
		}
		nextStrict := next.Accounting != nil && next.Accounting.StrictInputCountRequired
		if prepareErr = a.completeStrictAccounting(ctx, key, candidate, nextBody, nextSecret, next.ID, false, next.Accounting); prepareErr != nil {
			a.policyState.ReleaseProbe(nextCandidate)
			a.mu.Unlock()
			err = prepareErr
			break
		}
		if nextStrict {
			nextTokens = next.Accounting.EstimatedInputTokens + next.Accounting.EstimatedOutputTokens
			nextEstimate = "provider_input_count_plus_output_cap"
			next.AttemptStarted = time.Now().UTC()
		} else {
			var nextTokenErr error
			nextTokens, nextEstimate, nextTokenErr = reservationEstimate(nextBody, nextRaw)
			if nextTokenErr != nil && key.Limits.TPM != nil {
				a.policyState.ReleaseProbe(nextCandidate)
				a.mu.Unlock()
				err = &accountingError{422, "limits.tpm", nextTokenErr.Error()}
				break
			}
		}
		if ok, _ := a.checkTPM(key, nextTokens, time.Now()); !ok {
			a.policyState.ReleaseProbe(nextCandidate)
			a.mu.Unlock()
			err = &accountingError{429, "limits.tpm", "下一次尝试的token预留超过本地窗口限制"}
			break
		}
		next.ReservedTokens = nextTokens
		next.TokenReservationSource = nextEstimate
		if storeErr := a.Store.record(next); storeErr != nil {
			a.policyState.ReleaseProbe(nextCandidate)
			var budgetErr *accountingError
			if !errors.As(storeErr, &budgetErr) {
				a.markStorageFailure()
			}
			a.mu.Unlock()
			err = storeErr
			break
		}
		a.reserveTPM(key, next.ID, nextTokens, time.Now())
		a.accountActive[src.AccountID]--
		a.accountActive[candidate.AccountID]++
		a.runningSources[next.ID] = candidate.ID
		src = candidate
		selectedCandidate = nextCandidate
		rec = &next
		body, adapter, nativeDirect, raw, previous, stream = nextBody, nextAdapter, nextNative, nextRaw, nextPrevious, nextStream
		cloudBody = nextCloudBody
		secret, account = nextSecret, nextAccount
		a.mu.Unlock()
		req, err = prepareUpstream(ctx, src, body, raw, secret, account, stream, cfg.Codex)
		if err != nil {
			break
		}
		if protocol == "messages" && nativeDirect && r.Header.Get("Anthropic-Beta") != "" {
			req.Header.Set("Anthropic-Beta", r.Header.Get("Anthropic-Beta"))
		}
	}
	if err != nil {
		var budgetErr *accountingError
		if errors.As(err, &budgetErr) {
			accountingFailure(w, err)
			return
		}
		rec.Status = "failed"
		rec.ErrorStage = "connect_or_headers"
		code := 502
		var timeout net.Error
		if errors.As(err, &timeout) && timeout.Timeout() || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			code = 504
		}
		fail(w, code, "上游连接或响应头失败，未重试；上游执行结果可能未知", "")
		return
	}
	defer resp.Body.Close()
	resp.Body = &observedResponseBody{ReadCloser: resp.Body, count: &rec.UpstreamBytes}
	upstreamRetryAfter = resp.Header.Get("Retry-After")
	if route, routeErr := a.Store.route(key.RouteID); routeErr == nil {
		a.policyState.BindSession(route.RoutePolicy, policyInput, selectedCandidate, time.Now())
	}
	rec.HTTPStatus = resp.StatusCode
	rec.UpstreamRequestID = redact(resp.Header.Get("X-Request-Id"), secret, r.Header.Get("Authorization"))
	for _, h := range []string{"Retry-After", "X-Request-Id", "Content-Type"} {
		if v := resp.Header.Get(h); v != "" {
			w.Header().Set(h, redact(v, secret, r.Header.Get("Authorization")))
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	var stopWriteCancellation func() bool
	var writeCancellationDone chan struct{}
	defer func() {
		if stopWriteCancellation != nil {
			if !stopWriteCancellation() {
				<-writeCancellationDone
			}
		}
	}()
	writeDownstream := func(b []byte) error {
		if stopWriteCancellation == nil {
			writeCancellationDone = make(chan struct{})
			stopWriteCancellation = context.AfterFunc(ctx, func() {
				defer close(writeCancellationDone)
				_ = controller.SetWriteDeadline(time.Now())
			})
		}
		_ = controller.SetWriteDeadline(time.Now().Add(time.Duration(cfg.IdleTimeout) * time.Second))
		e := ctx.Err()
		if e != nil {
			_ = controller.SetWriteDeadline(time.Now())
		} else {
			var sent int
			sent, e = w.Write(b)
			rec.ResponseBytes += int64(sent)
		}
		if e != nil {
			rec.DeliveryStatus, rec.ErrorStage = "failed", "downstream_write"
		}
		return e
	}
	idle := time.AfterFunc(time.Duration(cfg.IdleTimeout)*time.Second, func() { idleExpired.Store(true); cancel() })
	defer idle.Stop()
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		rec.Status = "failed"
		rec.ErrorStage = "redirect"
		fail(w, 502, "上游重定向被拒绝，凭据未发送到新地址", "")
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, e := readLimited(resp.Body, cfg.MaxResponse)
		idle.Stop()
		rec.Status = "failed"
		rec.UpstreamStatus = "rejected"
		rec.ErrorStage = "upstream_http"
		rec.ErrorSummary = "上游返回错误，请核对来源认证、模型和 Retry-After"
		if e != nil {
			fail(w, 502, "上游错误响应无法完整读取", "")
			return
		}
		w.WriteHeader(resp.StatusCode)
		if e = writeDownstream([]byte(redact(string(b), secret, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")))); e != nil {
			rec.DeliveryStatus = "failed"
		} else {
			rec.DeliveryStatus = "completed"
		}
		return
	}
	compactItems := map[string]map[string]json.RawMessage{}
	observe := func(payload []byte, event string) error {
		var v map[string]json.RawMessage
		if json.Unmarshal(payload, &v) != nil {
			return nil
		}
		if nested, ok := v["response"]; ok {
			if json.Unmarshal(nested, &v) != nil {
				return nil
			}
		}
		if compactTrigger && event == "response.output_item.done" {
			var item map[string]json.RawMessage
			if json.Unmarshal(v["item"], &item) == nil {
				var kind string
				_ = json.Unmarshal(item["type"], &kind)
				if kind == "compaction" {
					index := string(v["output_index"])
					if index == "" {
						index = digest(encode(item))
					}
					compactItems[index] = item
				}
			}
		}
		if compactTrigger && event == "response.completed" {
			if len(compactItems) != 1 {
				return errors.New("Codex流式压缩必须收到恰好一个 compaction item")
			}
			items := []map[string]json.RawMessage{}
			for _, item := range compactItems {
				items = append(items, item)
			}
			if _, e := a.bindCompactOutput([]byte(encode(map[string]any{"output": items})), *rec, src); e != nil {
				return e
			}
		}
		var responseID string
		_ = json.Unmarshal(v["id"], &responseID)
		if responseID != "" && responseID != rec.ResponseID && src.NativeProtocol != "chat_completions" && src.NativeProtocol != "messages" {
			if err := a.Store.bind(*rec, responseID); err != nil {
				a.markStorageFailure()
				rec.ErrorStage = "binding_store"
				return err
			}
			rec.ResponseID = responseID
		}
		_ = json.Unmarshal(v["model"], &rec.ReportedModel)
		if usage, ok := v["usage"]; ok {
			mergeUsage(&rec.Usage, usage)
		}
		if src.Kind == "api_key" && src.NativeProtocol == "responses" && len(v["output"]) > 0 && strings.Contains(string(v["output"]), `"compaction"`) {
			if _, e := a.bindCompactOutput([]byte(encode(v)), *rec, src); e != nil {
				return e
			}
		}
		var status string
		_ = json.Unmarshal(v["status"], &status)
		if event == "response.completed" || event == "" && status == "completed" {
			if status == "" || status == "completed" {
				rec.Status = "succeeded"
				rec.UpstreamStatus = "completed"
			}
		}
		if event == "error" || event == "response.failed" || event == "response.incomplete" || status == "failed" || status == "incomplete" {
			rec.Status = "failed"
			rec.UpstreamStatus = status
			if status == "" {
				rec.UpstreamStatus = "failed"
			}
			rec.ErrorStage = "upstream_event"
			rec.ErrorSummary = "上游报告失败或未完成"
		}
		return nil
	}
	if !stream {
		b, e := readLimited(resp.Body, cfg.MaxResponse)
		idle.Stop()
		if e != nil {
			rec.Status = "failed"
			rec.ErrorStage = "response_body"
			code := 502
			if idleExpired.Load() || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				code = 504
			}
			fail(w, code, "上游响应读取失败、超时或超出限制；未重试", "")
			return
		}
		if !json.Valid(b) {
			rec.Status = "failed"
			rec.ErrorStage = "response_body"
			fail(w, 502, "上游未返回有效 JSON", "")
			return
		}
		observed := b
		if geminiDirect != nil {
			e = geminiDirect.observe(b)
			geminiDirect.finish()
		} else if src.NativeProtocol != "" && src.NativeProtocol != "responses" {
			if src.NativeProtocol == "gemini" {
				observed, e = geminiJSONToResponses(b, rec.ID, geminiBinding)
				b = observed
			} else if nativeDirect {
				observed, e = nativeMetadata(b, src.NativeProtocol)
			} else {
				observed, e = nativeJSON(b, src.NativeProtocol)
				if e == nil {
					b = observed
				}
			}
			if e != nil {
				// Conversion can reject opaque content after the provider has
				// already completed and reported billable usage.
				if src.NativeProtocol == "messages" || src.NativeProtocol == "chat_completions" {
					if metadata, metadataErr := nativeMetadata(b, src.NativeProtocol); metadataErr == nil {
						if observeErr := observe(metadata, ""); observeErr != nil {
							rec.Status = "failed"
							fail(w, 503, storageError().Error(), "")
							return
						}
					} else {
						var payload map[string]json.RawMessage
						_ = json.Unmarshal(b, &payload)
						mergeNativeUsage(&rec.Usage, payload["usage"], src.NativeProtocol)
						rec.ObservationStatus = "partial"
						observationSkipped = true
					}
				}
				rec.Status = "failed"
				rec.ErrorStage = "protocol_conversion"
				fail(w, 502, "原生响应缺少合法终态或无法转换", "")
				return
			}
		}
		if geminiDirect == nil {
			e = observe(observed, "")
		}
		if e != nil {
			rec.Status = "failed"
			fail(w, 503, storageError().Error(), "")
			return
		}
		if rec.Status == "dispatching" {
			rec.Status = "failed"
			rec.ErrorSummary = "上游响应没有完成状态"
		}
		if adapter != nil {
			b, e = adapter.finish(b)
			if e != nil {
				rec.Status, rec.ErrorStage, rec.ErrorSummary = "failed", "protocol_conversion", e.Error()
				fail(w, 502, "上游响应无法转换为请求协议，请查看请求记录", "")
				return
			}
			w.Header().Set("Content-Type", "application/json")
		}
		if protocol == "gemini" && !nativeDirect {
			b, e = responsesJSONToGemini(b)
			if e != nil {
				rec.Status = "failed"
				rec.ErrorStage = "protocol_conversion"
				fail(w, 502, "响应无法转换为 Gemini", "")
				return
			}
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(resp.StatusCode)
		if e := writeDownstream(b); e != nil {
			rec.Status, rec.DeliveryStatus, rec.ErrorStage = "failed", "failed", "downstream_write"
			rec.ErrorSummary = "上游响应已收到，但客户端接收失败"
		} else {
			rec.DeliveryStatus = "completed"
			if cache != nil && rec.Status == "succeeded" {
				a.mu.Lock()
				cacheErr := a.storeResponseCache(cache, b, w.Header().Get("Content-Type"), *rec)
				a.mu.Unlock()
				if cacheErr != nil {
					a.markStorageFailure()
				}
			}
		}
		return
	}
	mediaType, streamBody := sourceStream(src, resp)
	idle.Stop()
	if mediaType != "text/event-stream" {
		rec.Status = "failed"
		rec.ErrorStage = "content_type"
		rec.ErrorSummary = "流式请求没有收到可识别的 SSE 响应"
		fail(w, 502, "流式请求没有收到 SSE 响应", "")
		return
	}
	rec.Status = "streaming"
	rec.DeliveryStatus = "streaming"
	if err = a.Store.record(*rec); err != nil {
		a.markStorageFailure()
		fail(w, 503, storageError().Error(), "")
		return
	}
	if adapter == nil || adapter.stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(resp.StatusCode)
		committed = true
	} else {
		w.Header().Set("Content-Type", "application/json")
	}
	write := func(b []byte) error {
		if rec.FirstEvent == nil {
			t := time.Now().UTC()
			rec.FirstEvent = &t
		}
		e := writeDownstream(b)
		if e == nil {
			e = controller.Flush()
		}
		if e != nil {
			rec.DeliveryStatus, rec.ErrorStage = "failed", "downstream_write"
		}
		return e
	}
	processFrame := func(frame []byte) error {
		data, event := sseData(frame)
		if rec.FirstContentAt == nil && policySemanticOutput(data, event, "responses") {
			now := time.Now().UTC()
			rec.FirstContentAt = &now
		}
		if len(data) > 0 {
			if e := observe(data, event); e != nil {
				return e
			}
		}
		if geminiOutput != nil {
			converted, e := geminiOutput.Event(frame)
			if e != nil {
				rec.ErrorStage = "protocol_conversion"
				return e
			}
			if len(converted) == 0 {
				return nil
			}
			return write(converted)
		}
		if adapter != nil {
			converted, e := adapter.event(frame)
			if e != nil {
				rec.ErrorStage = "protocol_conversion"
				rec.ErrorSummary = e.Error()
				return e
			}
			if len(converted) == 0 {
				return nil
			}
			return write(converted)
		}
		return write(frame)
	}
	var native *nativeOutput
	var geminiNative *GeminiNativeStream
	if src.NativeProtocol != "" && src.NativeProtocol != "responses" && geminiDirect == nil {
		if src.NativeProtocol == "gemini" {
			geminiNative = newGeminiNativeStream(cfg.MaxResponse, rec.ID, geminiBinding)
			native = geminiNative.Native
		} else {
			native = newNativeOutput(src.NativeProtocol, cfg.MaxResponse, !nativeDirect)
		}
		if !nativeDirect {
			native.emit = processFrame
		}
	}
	observer := newSSE(cfg.MaxEvent, func(frame []byte) error {
		if geminiDirect != nil {
			data, _ := sseData(frame)
			if len(data) > 0 {
				if e := geminiDirect.observe(data); e != nil {
					return e
				}
				if rec.FirstContentAt == nil && policySemanticOutput(data, "", "gemini") {
					now := time.Now().UTC()
					rec.FirstContentAt = &now
				}
			}
			return write(frame)
		}
		if geminiNative != nil {
			return geminiNative.Frame(frame)
		}
		if native == nil {
			return processFrame(frame)
		}
		data, event := sseData(frame)
		if rec.FirstContentAt == nil && policySemanticOutput(data, event, src.NativeProtocol) {
			now := time.Now().UTC()
			rec.FirstContentAt = &now
		}
		frameErr := native.frame(frame)
		rec.Usage = native.usage
		if frameErr != nil {
			if rec.ErrorStage == "" {
				rec.ErrorStage = "protocol_conversion"
			}
			if !native.terminal {
				rec.ObservationStatus = "partial"
				observationSkipped = true
			}
			return frameErr
		}
		if nativeDirect {
			if native.terminal {
				payload := []byte(encode(native.response))
				if e := observe(payload, ""); e != nil {
					return e
				}
			}
			return write(frame)
		}
		return nil
	}, func(raw []byte) error {
		if geminiDirect != nil {
			geminiDirect.skipped = true
		}
		if adapter != nil || geminiOutput != nil || native != nil && !nativeDirect {
			rec.ErrorStage = "protocol_conversion"
			rec.ObservationStatus = "partial"
			observationSkipped = true
			return errors.New("SSE 事件超出转换观察上限或事件未完整结束")
		}
		return write(raw)
	})
	buf := make([]byte, 32<<10)
	for {
		idle.Reset(time.Duration(cfg.IdleTimeout) * time.Second)
		n, e := streamBody.Read(buf)
		idle.Stop() // Backpressure in the downstream writer is not upstream idleness.
		if n > 0 {
			if err = observer.Feed(buf[:n]); err != nil {
				rec.Status = "failed"
				if rec.ErrorStage == "" {
					rec.ErrorStage = "stream_or_storage"
				}
				break
			}
		}
		if e != nil {
			if e != io.EOF {
				err = e
			}
			break
		}
	}
	if err == nil {
		err = observer.End()
	}
	if err == nil && geminiNative != nil {
		err = geminiNative.End()
	}
	if err == nil && geminiOutput != nil {
		err = geminiOutput.End()
	}
	if err == nil && geminiDirect != nil {
		geminiDirect.finish()
	}
	if err == nil && native != nil && !native.terminal {
		err = errors.New("原生流缺少正式终态")
	}
	if observer.Skipped {
		observationSkipped = true
		rec.ObservationStatus = "partial"
	}
	if adapter != nil {
		if err == nil {
			var output []byte
			output, err = adapter.finish(adapter.final)
			if err != nil {
				rec.ErrorStage = "protocol_conversion"
				rec.ErrorSummary = err.Error()
			} else if adapter.stream {
				err = write(output)
			} else {
				w.WriteHeader(resp.StatusCode)
				committed = true
				err = writeDownstream(output)
			}
		}
		if err != nil {
			rec.Status = "failed"
			if !committed {
				status := 502
				if idleExpired.Load() || errors.Is(ctx.Err(), context.DeadlineExceeded) {
					status = 504
				}
				fail(w, status, "上游流未完整完成或无法转换，请查看请求记录", "")
				return
			}
			if adapter.stream && rec.ErrorStage != "downstream_write" {
				_ = write(adapter.streamError(rec.ID))
			}
		}
	}
	if err != nil && geminiOutput != nil && committed && rec.ErrorStage != "downstream_write" {
		_ = write(geminiConversionStreamError(rec.ID))
	}
	if err == nil {
		rec.DeliveryStatus = "completed"
		if rec.Status == "streaming" && observer.Skipped {
			rec.Status, rec.ErrorStage = "unverified", "observation_limit"
			rec.ErrorSummary = "流已转发，终态超出观察范围，无法确认上游结果"
		}
	}
	if err != nil || rec.Status == "streaming" {
		rec.Status = "failed"
		if rec.DeliveryStatus == "streaming" {
			rec.DeliveryStatus = "partial"
		}
		if rec.ErrorSummary == "" {
			rec.ErrorSummary = "流提前结束或传输失败；未重试，上游最终结果可能未知"
		}
		if rec.ErrorStage == "" {
			rec.ErrorStage = "stream"
		}
		if committed && protocol == "gemini" && geminiDirect != nil && rec.ErrorStage != "downstream_write" {
			_ = write(geminiConversionStreamError(rec.ID))
		}
		if committed && adminSource == "" && protocol != "gemini" {
			panic(http.ErrAbortHandler)
		}
	}
	return
}
func redact(value string, secrets ...string) string {
	for _, s := range secrets {
		if s != "" {
			value = strings.ReplaceAll(value, s, "[redacted]")
		}
	}
	return value
}

type discardResponse struct {
	header    http.Header
	status    int
	errorBody bytes.Buffer
}

func (d *discardResponse) Header() http.Header { return d.header }
func (d *discardResponse) WriteHeader(n int)   { d.status = n }
func (d *discardResponse) Write(b []byte) (int, error) {
	if d.status >= 400 && d.errorBody.Len()+len(b) <= 64<<10 {
		_, _ = d.errorBody.Write(b)
	}
	return len(b), nil
}
func (d *discardResponse) Flush() {}
func (a *App) testSource(w http.ResponseWriter, r *http.Request, source string) {
	var in struct {
		Model        string `json:"model"`
		Continuation bool   `json:"continuation"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.mu.Lock()
	src, err := a.Store.source(source)
	a.mu.Unlock()
	if err != nil || src.Deleted {
		fail(w, 404, "来源不存在", "")
		return
	}
	if in.Model == "" && len(src.Models) > 0 {
		in.Model = src.Models[0]
	}
	if in.Continuation && src.Kind != "api_key" {
		fail(w, 400, "订阅来源不支持服务端状态续接", "continuation")
		return
	}
	body := map[string]any{"model": in.Model, "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly OK."}}}}, "stream": src.Kind == "codex_subscription", "store": in.Continuation}
	if src.NativeProtocol == "messages" {
		body["max_output_tokens"] = 32
	}
	req := r.Clone(r.Context())
	req.Body = io.NopCloser(strings.NewReader(encode(body)))
	sink := &discardResponse{header: make(http.Header)}
	var result *Record
	func() {
		defer func() {
			if p := recover(); p != nil && p != http.ErrAbortHandler {
				panic(p)
			}
		}()
		result = a.forward(sink, req, source)
	}()
	if result == nil {
		if sink.status >= 400 && sink.errorBody.Len() > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Gateway-Request-Id", sink.header.Get("X-Gateway-Request-Id"))
			w.WriteHeader(sink.status)
			_, _ = w.Write(sink.errorBody.Bytes())
		} else {
			fail(w, 503, "测试未完成，请检查来源配置、认证与存储", "")
		}
		return
	}
	if in.Continuation && result.Status == "succeeded" && result.ResponseID != "" {
		first := result
		body["previous_response_id"] = first.ResponseID
		req.Body = io.NopCloser(strings.NewReader(encode(body)))
		result = a.forward(&discardResponse{header: make(http.Header)}, req, source)
		if result != nil && result.Status == "succeeded" {
			a.mu.Lock()
			current, e := a.Store.source(source)
			if e == nil && current.Generation == first.Generation && current.Version == src.Version {
				current.Continuation = true
				current.Verification.Capabilities = append(current.Verification.Capabilities, "previous_response_id")
				e = a.Store.saveSource(current)
			}
			a.mu.Unlock()
			if e != nil {
				fail(w, 503, storageError().Error(), "")
				return
			}
		}
		if result == nil {
			fail(w, 503, "续接测试未完成", "")
			return
		}
	}
	writeJSON(w, 200, map[string]any{"request": result, "scope": "synthetic_text_only", "tool_loop_verified": false})
}
