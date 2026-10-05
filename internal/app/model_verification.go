package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

type verificationResponse struct {
	header http.Header
	body   bytes.Buffer
}

func (w *verificationResponse) Header() http.Header { return w.header }
func (w *verificationResponse) WriteHeader(int)     {}
func (w *verificationResponse) Flush()              {}
func (w *verificationResponse) Write(body []byte) (int, error) {
	if w.body.Len()+len(body) > 1<<20 {
		return 0, errors.New("验证输出超出1MiB")
	}
	return w.body.Write(body)
}
func verificationText(body []byte, protocol string) bool {
	var v struct {
		Output  []struct{ Content []struct{ Type, Text string } }
		Choices []struct{ Message struct{ Content string } }
		Content []struct{ Type, Text string }
	}
	if json.Unmarshal(body, &v) != nil {
		return false
	}
	switch protocol {
	case "chat_completions":
		for _, c := range v.Choices {
			if c.Message.Content != "" {
				return true
			}
		}
	case "messages":
		for _, c := range v.Content {
			if c.Type == "text" && c.Text != "" {
				return true
			}
		}
	default:
		for _, item := range v.Output {
			for _, c := range item.Content {
				if c.Type == "output_text" && c.Text != "" {
					return true
				}
			}
		}
	}
	return false
}

// Successful delivery alone is not proof that a text test produced text.
// This reads the bounded verification sink, never changes provider wire data.
func verificationStreamText(body []byte, protocol string) bool {
	hasText := false
	observer := newSSE(1<<20, func(frame []byte) error {
		data, kind := sseData(frame)
		if len(data) == 0 || string(data) == "[DONE]" {
			return nil
		}
		var event struct {
			Delta        json.RawMessage `json:"delta"`
			Item         json.RawMessage `json:"item"`
			Response     json.RawMessage `json:"response"`
			ContentBlock json.RawMessage `json:"content_block"`
			Choices      []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &event) != nil {
			return errors.New("验证输出事件无法解析")
		}
		switch protocol {
		case "chat_completions":
			for _, choice := range event.Choices {
				hasText = hasText || choice.Delta.Content != ""
			}
		case "messages":
			if kind == "content_block_start" {
				hasText = hasText || verificationText([]byte(encode(map[string]any{"content": []json.RawMessage{event.ContentBlock}})), protocol)
			}
			if kind == "content_block_delta" {
				var delta struct{ Type, Text string }
				if json.Unmarshal(event.Delta, &delta) == nil && delta.Type == "text_delta" {
					hasText = hasText || delta.Text != ""
				}
			}
		default:
			switch kind {
			case "response.output_text.delta":
				var text string
				if json.Unmarshal(event.Delta, &text) == nil {
					hasText = hasText || text != ""
				}
			case "response.output_item.done":
				hasText = hasText || verificationText([]byte(encode(map[string]any{"output": []json.RawMessage{event.Item}})), protocol)
			case "response.completed", "response.incomplete":
				hasText = hasText || verificationText(event.Response, protocol)
			}
		}
		return nil
	}, func([]byte) error { return errors.New("验证输出事件超限或未完整结束") })
	return observer.Feed(body) == nil && observer.End() == nil && hasText
}

type ModelVerificationResult struct {
	Protocol          string    `json:"protocol"`
	Feature           string    `json:"feature"`
	Status            string    `json:"status"`
	RequestIDs        []string  `json:"request_ids"`
	SourceGeneration  int       `json:"source_generation"`
	AccountGeneration int       `json:"account_generation"`
	ModelVersion      int       `json:"model_version"`
	ObservedAt        time.Time `json:"observed_at"`
}

// A verification is an explicit operation. It uses the same admission,
// transport and financial records as an ordinary request, never a hidden probe.
func (a *App) verifyModelAPI(w http.ResponseWriter, r *http.Request, modelID string) {
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return
	}
	var in struct {
		Protocol           string   `json:"protocol"`
		Features           []string `json:"features"`
		SourceID           string   `json:"source_id"`
		ClientKeyID        string   `json:"client_key_id"`
		ExpectedGeneration int      `json:"expected_source_generation"`
		Client             *struct {
			Kind    string `json:"kind"`
			Version string `json:"version"`
		} `json:"client"`
	}
	if !decode(w, r, &in) {
		return
	}
	a.mu.Lock()
	model, err := a.Store.model(modelID)
	src, se := a.Store.source(model.SourceID)
	a.mu.Unlock()
	if err != nil || se != nil || src.Deleted {
		fail(w, 404, "模型或来源不存在", "")
		return
	}
	if in.SourceID != "" && in.SourceID != src.ID || in.ExpectedGeneration != src.Generation {
		fail(w, 409, "来源代次已改变，请刷新后验证", "expected_source_generation")
		return
	}
	if in.Protocol == "" {
		in.Protocol = "responses"
	}
	if in.Protocol != "responses" && in.Protocol != "chat_completions" && in.Protocol != "messages" {
		fail(w, 422, "此验证入口支持 Responses、Chat 或 Messages；原生 Gemini 使用其独立入口", "protocol")
		return
	}
	if len(in.Features) == 0 {
		in.Features = []string{"text_json"}
		if in.Protocol == "responses" && src.Kind == "codex_subscription" {
			in.Features = []string{"text_sse"}
		}
	}
	if len(in.Features) > 4 {
		fail(w, 400, "一次验证最多4项", "features")
		return
	}
	seen := map[string]bool{}
	var backgroundKey ClientKey
	for _, feature := range in.Features {
		if seen[feature] {
			fail(w, 400, "验证项重复", "features")
			return
		}
		seen[feature] = true
		if feature != "text_json" && feature != "text_sse" && feature != "continuation" && feature != "background" {
			fail(w, 422, "此功能尚无可核验的测试动作，请使用该原生操作的独立测试", "features")
			return
		}
		if feature == "text_json" && in.Protocol == "responses" && src.Kind == "codex_subscription" {
			fail(w, 422, "订阅来源的原生 Responses 仅支持流式文本，请选择 text_sse", "features")
			return
		}
		if feature == "continuation" && (in.Protocol != "responses" || src.NativeProtocol != "responses" || src.Kind != "api_key") {
			fail(w, 422, "续接验证需要原生 Responses API 来源", "features")
			return
		}
		if feature == "background" {
			if in.Protocol != "responses" {
				fail(w, 422, "后台验证需要原生 Responses", "protocol")
				return
			}
			var raw string
			if in.ClientKeyID == "" || a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", in.ClientKeyID).Scan(&raw) != nil || json.Unmarshal([]byte(raw), &backgroundKey) != nil {
				fail(w, 400, "后台验证须选择已有客户端 Key，使用其权限和预算", "client_key_id")
				return
			}
			if err := validateBackgroundVerification(src, model, backgroundKey); err != nil {
				resourceFail(w, err)
				return
			}
		}
	}
	protocol := in.Protocol
	features := append([]string(nil), in.Features...)
	a.startLocalOperation(w, r, "model_verification", func(ctx context.Context, _ string) (any, error) {
		results := []ModelVerificationResult{}
		for _, feature := range features {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			result := ModelVerificationResult{Protocol: protocol, Feature: feature, Status: "failed", SourceGeneration: src.Generation, AccountGeneration: src.AccountGeneration, ModelVersion: model.Version, ObservedAt: time.Now().UTC(), RequestIDs: []string{}}
			if feature == "background" {
				record, passed, err := a.verifyBackground(ctx, src, model, backgroundKey)
				if record != nil {
					result.RequestIDs = append(result.RequestIDs, record.ID)
					result.AccountGeneration = record.AccountGeneration
				}
				if err != nil && ctx.Err() != nil {
					return nil, ctx.Err()
				}
				if passed {
					result.Status = "passed"
				}
				results = append(results, result)
				continue
			}
			body := map[string]any{"model": model.UpstreamModel, "input": []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "input_text", "text": "Reply with exactly OK."}}}}, "stream": feature == "text_sse"}
			if src.NativeProtocol == "messages" {
				body["max_output_tokens"] = 32
			}
			path := "/v1/responses"
			if protocol != "responses" {
				delete(body, "input")
				body["messages"] = []any{map[string]any{"role": "user", "content": "Reply with exactly OK."}}
				path = "/v1/chat/completions"
				if protocol == "messages" {
					path = "/v1/messages"
					body["max_tokens"] = 32
				}
			}
			if feature == "continuation" {
				body["store"] = true
			}
			sink := &verificationResponse{header: make(http.Header)}
			run := func() *Record {
				sink = &verificationResponse{header: make(http.Header)}
				req, _ := http.NewRequestWithContext(ctx, "POST", path, io.NopCloser(strings.NewReader(encode(body))))
				req.Header.Set("X-Cove-Verification", "model")
				return a.forward(sink, req, src.ID)
			}
			record := run()
			if record != nil {
				result.RequestIDs = append(result.RequestIDs, record.ID)
				result.AccountGeneration = record.AccountGeneration
			}
			passed := record != nil && record.Status == "succeeded" && record.DeliveryStatus == "completed"
			if feature == "text_json" || feature == "text_sse" {
				if feature == "text_sse" {
					passed = passed && verificationStreamText(sink.body.Bytes(), protocol)
				} else {
					passed = passed && verificationText(sink.body.Bytes(), protocol)
				}
			}
			if feature == "continuation" && passed {
				if record.ResponseID == "" {
					passed = false
				} else {
					body["previous_response_id"] = record.ResponseID
					record = run()
					if record != nil {
						result.RequestIDs = append(result.RequestIDs, record.ID)
						if record.AccountGeneration != result.AccountGeneration {
							return nil, errors.New("验证期间配置已改变，旧结果保留于请求记录，请重新验证")
						}
					}
					passed = record != nil && record.Status == "succeeded"
				}
			}
			if passed {
				result.Status = "passed"
			}
			results = append(results, result)
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		current, err := a.Store.source(src.ID)
		latest, me := a.Store.model(model.ID)
		if err != nil || me != nil {
			return nil, storageError()
		}
		if current.Deleted || current.Version != src.Version || current.Generation != src.Generation || latest.Version != model.Version {
			return nil, errors.New("验证期间配置已改变，旧结果保留于请求记录，请重新验证")
		}
		for _, result := range results {
			if result.AccountGeneration != current.AccountGeneration {
				return nil, errors.New("验证期间配置已改变，旧结果保留于请求记录，请重新验证")
			}
		}
		latest.Verification = "passed"
		sourceChanged := false
		for _, result := range results {
			if result.Status != "passed" {
				latest.Verification = "failed"
			}
			if result.Feature == "background" {
				sourceChanged = true
				current.Verification.TestedAt = &result.ObservedAt
				current.Verification.Model = model.UpstreamModel
				current.Verification.Capabilities = removeCapability(current.Verification.Capabilities, "background")
				if result.Status == "passed" {
					current.Verification.Status = "passed"
					current.Verification.Capabilities = append(current.Verification.Capabilities, "background")
				} else if len(current.Verification.Capabilities) == 0 {
					current.Verification.Status = "failed"
				}
				if len(result.RequestIDs) > 0 {
					current.Verification.RequestID = result.RequestIDs[0]
				}
			}
		}
		latest.VerificationResults = results
		for _, result := range results {
			if result.Feature == "continuation" && result.Status == "passed" {
				current.Continuation = true
				sourceChanged = true
			}
		}
		tx, err := a.Store.DB.Begin()
		if err == nil {
			defer tx.Rollback()
			_, err = tx.Exec("UPDATE source_models SET data=? WHERE id=?", encode(latest), latest.ID)
			if err == nil && sourceChanged {
				_, err = tx.Exec("UPDATE sources SET data=? WHERE id=?", encode(current), current.ID)
			}
			if err == nil {
				err = tx.Commit()
			}
		}
		if err != nil {
			a.markStorageFailure()
			return nil, storageError()
		}
		return map[string]any{"model_id": model.ID, "results": results, "passed": latest.Verification == "passed"}, nil
	})
}

func removeCapability(values []string, target string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

func (s *Store) effectiveModelVerification(m *SourceModel) {
	if len(m.VerificationResults) == 0 {
		return
	}
	source, err := s.source(m.SourceID)
	if err != nil {
		m.Verification = "stale"
		return
	}
	for _, v := range m.VerificationResults {
		if v.SourceGeneration != source.Generation || v.AccountGeneration != source.AccountGeneration || v.ModelVersion != m.Version {
			m.Verification = "stale"
			return
		}
	}
}
