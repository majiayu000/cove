package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Compatibility is a client-side protocol boundary. All model dispatch still
// uses the existing Responses path, admission, credential and accounting code.
type compatRequest struct {
	Model               string            `json:"model"`
	Messages            []compatMessage   `json:"messages"`
	Stream              bool              `json:"stream"`
	Tools               []json.RawMessage `json:"tools"`
	ToolChoice          json.RawMessage   `json:"tool_choice"`
	Temperature         *float64          `json:"temperature"`
	TopP                *float64          `json:"top_p"`
	MaxTokens           *int64            `json:"max_tokens"`
	MaxCompletionTokens *int64            `json:"max_completion_tokens"`
	Parallel            *bool             `json:"parallel_tool_calls"`
	ReasoningEffort     string            `json:"reasoning_effort"`
	N                   *int              `json:"n"`
	StreamOptions       *struct {
		IncludeUsage bool `json:"include_usage"`
	} `json:"stream_options"`
	System         json.RawMessage `json:"system"`
	ResponseFormat json.RawMessage `json:"response_format"`
	Metadata       json.RawMessage `json:"metadata"`
	OutputConfig   json.RawMessage `json:"output_config"`
}
type compatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"`
	Refusal    *string         `json:"refusal"`
	ToolCallID string          `json:"tool_call_id"`
	ToolCalls  []struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	} `json:"tool_calls"`
}
type messageBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type unsupportedFeatureError struct{ message string }

func (e *unsupportedFeatureError) Error() string { return e.message }
func unsupportedFeature(message string) error    { return &unsupportedFeatureError{message} }
func requestErrorStatus(err error) int {
	var unsupported *unsupportedFeatureError
	if errors.As(err, &unsupported) {
		return 422
	}
	return 400
}

func strictJSON(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") && json.Valid(raw) {
			return unsupportedFeature("兼容接口不支持字段: " + strings.TrimPrefix(err.Error(), "json: unknown field "))
		}
		return fmt.Errorf("兼容接口结构无效: %w", err)
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("请求必须为单个 JSON 值")
	}
	return nil
}

func compatInput(raw []byte, protocol string, src Source) (map[string]json.RawMessage, *compatOutput, error) {
	var in compatRequest
	if err := strictJSON(raw, &in); err != nil {
		return nil, nil, err
	}
	if in.Model == "" || len(in.Messages) == 0 {
		return nil, nil, errors.New("model 与非空 messages 必填")
	}
	if in.N != nil && *in.N != 1 {
		return nil, nil, unsupportedFeature("兼容接口仅支持 n=1")
	}
	if in.MaxTokens != nil && in.MaxCompletionTokens != nil {
		return nil, nil, errors.New("不能同时设置 max_tokens 和 max_completion_tokens")
	}
	if in.StreamOptions != nil && !in.Stream {
		return nil, nil, errors.New("stream_options 仅适用于流式请求")
	}
	metadataAdjusted := false
	effortAdjusted := ""
	if len(in.OutputConfig) > 0 {
		if protocol != "messages" {
			return nil, nil, unsupportedFeature("output_config 仅适用于 Messages")
		}
		var config struct {
			Effort string `json:"effort"`
		}
		if !jsonObject(in.OutputConfig) {
			return nil, nil, errors.New("output_config 必须是对象")
		}
		if err := strictJSON(in.OutputConfig, &config); err != nil {
			return nil, nil, err
		}
		if config.Effort == "" {
			return nil, nil, errors.New("output_config.effort 必填")
		}
		if src.Kind != "codex_subscription" || !src.AllowParameterAdjustment {
			return nil, nil, unsupportedFeature("Anthropic effort 转换需要来源允许参数调整；同名等级不保证相同思考量")
		}
		effortAdjusted = config.Effort
	}
	if len(in.Metadata) > 0 {
		if protocol != "messages" {
			return nil, nil, unsupportedFeature("Chat 兼容入口不支持 metadata，请使用原生 Responses")
		}
		var metadata struct {
			UserID *string `json:"user_id"`
		}
		if !jsonObject(in.Metadata) {
			return nil, nil, errors.New("Messages metadata 必须是对象")
		}
		if err := strictJSON(in.Metadata, &metadata); err != nil {
			return nil, nil, err
		}
		if metadata.UserID != nil && len([]rune(*metadata.UserID)) > 512 {
			return nil, nil, errors.New("Messages metadata.user_id 最长为 512 个字符")
		}
		if src.Kind != "codex_subscription" || !src.AllowParameterAdjustment {
			return nil, nil, unsupportedFeature("订阅无法转发 Anthropic metadata.user_id；请在来源设置中启用参数调整，或使用原生 Messages 来源")
		}
		metadataAdjusted = true
	}
	if protocol == "messages" {
		if in.MaxTokens == nil || *in.MaxTokens <= 0 {
			return nil, nil, errors.New("Messages 兼容入口要求正整数 max_tokens；不支持仅填充缓存")
		}
		if in.MaxCompletionTokens != nil || in.N != nil || in.StreamOptions != nil || in.Parallel != nil || in.ReasoningEffort != "" || len(in.ResponseFormat) > 0 {
			return nil, nil, errors.New("Messages 请求包含 Chat 专属字段")
		}
		if in.Messages[len(in.Messages)-1].Role == "assistant" {
			return nil, nil, unsupportedFeature("暂不支持 assistant 预填充，请以 user 或工具结果结束历史")
		}
	} else if len(in.System) > 0 {
		return nil, nil, errors.New("Chat 请通过 system/developer 消息提供指令")
	}
	items := []any{}
	systemMessageAdjusted := false
	addText := func(role, text string) {
		if role == "system" && chatGPTDirectSource(src) {
			role = "developer"
		}
		items = append(items, map[string]any{"role": role, "content": text})
	}
	if len(in.System) > 0 {
		txt, err := compatText(in.System)
		if err != nil {
			return nil, nil, err
		}
		addText("system", txt)
	}
	for _, m := range in.Messages {
		if protocol == "messages" {
			if m.Role == "system" {
				if src.Kind != "codex_subscription" || !src.AllowParameterAdjustment {
					return nil, nil, unsupportedFeature("Messages 中途 system 指令转换需要来源允许参数调整；工具增删与 turn-scoped 清理不支持")
				}
				systemMessageAdjusted = true
			}
			if m.Role != "user" && m.Role != "assistant" && m.Role != "system" || len(m.ToolCalls) > 0 || m.ToolCallID != "" || m.Refusal != nil {
				return nil, nil, errors.New("Messages 只接受 user/assistant 和内容块工具历史")
			}
			var text string
			if json.Unmarshal(m.Content, &text) == nil && string(m.Content) != "null" {
				addText(m.Role, text)
				continue
			}
			var blocks []messageBlock
			if err := strictJSON(m.Content, &blocks); err != nil {
				return nil, nil, err
			}
			if len(blocks) == 0 {
				return nil, nil, errors.New("Messages content 必须是文本或非空内容块")
			}
			for _, b := range blocks {
				switch b.Type {
				case "text":
					if b.ID != "" || b.Name != "" || b.Input != nil || b.ToolUseID != "" || b.Content != nil || b.IsError {
						return nil, nil, errors.New("text 内容块包含不支持字段")
					}
					addText(m.Role, b.Text)
				case "tool_use":
					if m.Role != "assistant" || b.ID == "" || b.Name == "" || !jsonObject(b.Input) || b.Text != "" || b.ToolUseID != "" || b.Content != nil || b.IsError {
						return nil, nil, errors.New("tool_use 必须包含 id/name/对象 input，且属于 assistant")
					}
					items = append(items, map[string]any{"type": "function_call", "call_id": b.ID, "name": b.Name, "arguments": string(b.Input)})
				case "tool_result":
					if m.Role != "user" || b.ToolUseID == "" || b.ID != "" || b.Name != "" || b.Input != nil || b.Text != "" {
						return nil, nil, errors.New("tool_result 必须属于 user 并提供 tool_use_id")
					}
					text := ""
					if len(b.Content) > 0 {
						var err error
						text, err = compatText(b.Content)
						if err != nil {
							return nil, nil, err
						}
					}
					if b.IsError {
						text = encode(map[string]any{"is_error": true, "content": text})
					}
					items = append(items, map[string]any{"type": "function_call_output", "call_id": b.ToolUseID, "output": text})
				default:
					return nil, nil, unsupportedFeature("兼容接口仅支持 text、tool_use、tool_result；其他模态和 thinking 请使用原生协议")
				}
			}
			continue
		}
		if m.Role != "system" && m.Role != "developer" && m.Role != "user" && m.Role != "assistant" && m.Role != "tool" {
			return nil, nil, errors.New("Chat 消息角色不支持")
		}
		if m.Role != "assistant" && (len(m.ToolCalls) > 0 || m.Refusal != nil) || m.Role != "tool" && m.ToolCallID != "" {
			return nil, nil, errors.New("工具字段与消息角色不匹配")
		}
		if m.Role == "tool" {
			text, err := compatText(m.Content)
			if err != nil || m.ToolCallID == "" {
				return nil, nil, errors.New("tool 消息需要 tool_call_id 与文本 content")
			}
			items = append(items, map[string]any{"type": "function_call_output", "call_id": m.ToolCallID, "output": text})
			continue
		}
		if len(m.Content) > 0 && string(m.Content) != "null" {
			text, err := compatText(m.Content)
			if err != nil {
				return nil, nil, err
			}
			addText(m.Role, text)
		} else if len(m.ToolCalls) == 0 && m.Refusal == nil {
			return nil, nil, errors.New("消息缺少 content 或工具调用")
		}
		if m.Refusal != nil {
			items = append(items, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "refusal", "refusal": *m.Refusal}}})
		}
		for _, call := range m.ToolCalls {
			if call.Type != "function" || call.ID == "" || call.Function.Name == "" || !jsonObject([]byte(call.Function.Arguments)) {
				return nil, nil, errors.New("仅支持带有效对象 arguments 的 function 工具调用")
			}
			items = append(items, map[string]any{"type": "function_call", "call_id": call.ID, "name": call.Function.Name, "arguments": call.Function.Arguments})
		}
	}
	out := map[string]json.RawMessage{"model": json.RawMessage(encode(in.Model)), "input": json.RawMessage(encode(items)), "stream": json.RawMessage(encode(in.Stream || src.Kind == "codex_subscription")), "store": json.RawMessage("false")}
	for key, value := range map[string]*float64{"temperature": in.Temperature, "top_p": in.TopP} {
		if value != nil {
			out[key] = json.RawMessage(encode(*value))
		}
	}
	limit := in.MaxTokens
	if in.MaxCompletionTokens != nil {
		limit = in.MaxCompletionTokens
	}
	if limit != nil {
		if *limit <= 0 {
			return nil, nil, errors.New("输出 token 上限必须为正整数")
		}
		if src.Kind == "codex_subscription" {
			if !src.AllowParameterAdjustment {
				return nil, nil, unsupportedFeature("订阅无法兑现 max_tokens 输出 token 硬上限；请在来源设置中启用参数调整，或选择支持上限的 API 来源")
			}
		} else {
			out["max_output_tokens"] = json.RawMessage(encode(*limit))
		}
	}
	if in.Parallel != nil {
		out["parallel_tool_calls"] = json.RawMessage(encode(*in.Parallel))
	}
	if in.ReasoningEffort != "" {
		out["reasoning"] = json.RawMessage(encode(map[string]string{"effort": in.ReasoningEffort}))
	}
	if effortAdjusted != "" {
		out["reasoning"] = json.RawMessage(encode(map[string]string{"effort": effortAdjusted}))
	}
	if len(in.ResponseFormat) > 0 {
		var format struct {
			Type   string `json:"type"`
			Schema *struct {
				Name        string          `json:"name"`
				Description *string         `json:"description"`
				Schema      json.RawMessage `json:"schema"`
				Strict      *bool           `json:"strict"`
			} `json:"json_schema"`
		}
		if strictJSON(in.ResponseFormat, &format) != nil {
			return nil, nil, errors.New("response_format 无效")
		}
		v := map[string]any{"type": format.Type}
		switch format.Type {
		case "text", "json_object":
			if format.Schema != nil {
				return nil, nil, errors.New("response_format 类型与 schema 不匹配")
			}
		case "json_schema":
			if format.Schema == nil || format.Schema.Name == "" || !jsonObject(format.Schema.Schema) {
				return nil, nil, errors.New("json_schema 需要 name 和对象 schema")
			}
			v["name"] = format.Schema.Name
			v["schema"] = format.Schema.Schema
			if format.Schema.Description != nil {
				v["description"] = format.Schema.Description
			}
			if format.Schema.Strict != nil {
				v["strict"] = format.Schema.Strict
			}
		default:
			return nil, nil, unsupportedFeature("response_format 类型不支持")
		}
		out["text"] = json.RawMessage(encode(map[string]any{"format": v}))
	}
	tools := []any{}
	for _, raw := range in.Tools {
		var tool struct {
			Type        string          `json:"type"`
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Input       json.RawMessage `json:"input_schema"`
			Strict      *bool           `json:"strict"`
			Function    *struct {
				Name        string          `json:"name"`
				Description string          `json:"description"`
				Parameters  json.RawMessage `json:"parameters"`
				Strict      *bool           `json:"strict"`
			} `json:"function"`
		}
		if err := strictJSON(raw, &tool); err != nil {
			return nil, nil, err
		}
		name, description, schema, strict := tool.Name, tool.Description, tool.Input, tool.Strict
		if protocol == "chat_completions" {
			if tool.Type != "function" || tool.Function == nil || tool.Name != "" || tool.Input != nil || tool.Description != "" || tool.Strict != nil {
				return nil, nil, unsupportedFeature("Chat 仅支持 function 工具定义")
			}
			name, description, schema, strict = tool.Function.Name, tool.Function.Description, tool.Function.Parameters, tool.Function.Strict
			if schema == nil {
				schema = json.RawMessage(`{"type":"object","properties":{}}`)
			}
		} else if tool.Function != nil || tool.Type != "" && tool.Type != "custom" {
			return nil, nil, unsupportedFeature("Messages 仅支持客户端自定义工具")
		}
		if name == "" || !jsonObject(schema) {
			return nil, nil, errors.New("工具需要 name 和对象参数 schema")
		}
		v := map[string]any{"type": "function", "name": name, "description": description, "parameters": schema}
		// Responses defaults can be stricter than the input APIs. Preserve their
		// non-strict default instead of silently imposing strict schema rules.
		v["strict"] = strict != nil && *strict
		tools = append(tools, v)
	}
	if len(tools) > 0 {
		out["tools"] = json.RawMessage(encode(tools))
	}
	if len(in.ToolChoice) > 0 {
		choice, parallel, err := compatToolChoice(in.ToolChoice, protocol)
		if err != nil {
			return nil, nil, err
		}
		out["tool_choice"] = json.RawMessage(encode(choice))
		if named, ok := choice.(map[string]string); ok {
			found := false
			for _, tool := range tools {
				if tool.(map[string]any)["name"] == named["name"] {
					found = true
				}
			}
			if !found {
				return nil, nil, errors.New("tool_choice 指定名称未在 tools 中声明")
			}
		}
		if parallel != nil {
			out["parallel_tool_calls"] = json.RawMessage(encode(*parallel))
		}
	}
	adapter := &compatOutput{protocol: protocol, stream: in.Stream, model: in.Model, includeUsage: in.StreamOptions != nil && in.StreamOptions.IncludeUsage, blocks: map[string]*compatBlock{}}
	if metadataAdjusted {
		adapter.adjustments = append(adapter.adjustments, "anthropic_metadata_not_forwarded")
	}
	if effortAdjusted != "" {
		adapter.adjustments = append(adapter.adjustments, "anthropic_effort_mapped")
	}
	if systemMessageAdjusted {
		adapter.adjustments = append(adapter.adjustments, "anthropic_system_message_mapped")
	}
	if src.Kind == "codex_subscription" && limit != nil {
		adapter.adjustments = append(adapter.adjustments, "output_limit_not_enforced")
	}
	if protocol == "messages" && in.Stream {
		estimate, err := estimateTokens(encode(items) + encode(tools))
		if err != nil {
			return nil, nil, unsupportedFeature("固定 tokenizer 不可用，请选择原生 Messages 或非流式入口")
		}
		adapter.wireInput = &estimate
	}
	return out, adapter, nil
}

func jsonObject(raw []byte) bool {
	var v map[string]json.RawMessage
	return json.Unmarshal(raw, &v) == nil && v != nil
}
func compatText(raw []byte) (string, error) {
	var text string
	if string(raw) != "null" && json.Unmarshal(raw, &text) == nil {
		return text, nil
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := strictJSON(raw, &blocks); err != nil || blocks == nil {
		return "", unsupportedFeature("只支持文本 content，不能转换图像、音频、签名思考或缓存标记")
	}
	var result strings.Builder
	for _, b := range blocks {
		if b.Type != "text" {
			return "", unsupportedFeature("仅支持 text 内容块")
		}
		result.WriteString(b.Text)
	}
	return result.String(), nil
}
func compatToolChoice(raw []byte, protocol string) (any, *bool, error) {
	var simple string
	if protocol == "chat_completions" && json.Unmarshal(raw, &simple) == nil {
		if simple == "auto" || simple == "none" || simple == "required" {
			return simple, nil, nil
		}
		return nil, nil, unsupportedFeature("tool_choice 不支持")
	}
	var v struct {
		Type            string `json:"type"`
		Name            string `json:"name"`
		DisableParallel *bool  `json:"disable_parallel_tool_use"`
		Function        *struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := strictJSON(raw, &v); err != nil {
		return nil, nil, err
	}
	if protocol == "chat_completions" {
		if v.Type != "function" || v.Function == nil || v.Function.Name == "" || v.Name != "" || v.DisableParallel != nil {
			return nil, nil, errors.New("Chat tool_choice 结构无效")
		}
		return map[string]string{"type": "function", "name": v.Function.Name}, nil, nil
	}
	if v.Function != nil {
		return nil, nil, errors.New("Messages tool_choice 结构无效")
	}
	var parallel *bool
	if v.DisableParallel != nil {
		value := !*v.DisableParallel
		parallel = &value
	}
	if v.Type == "tool" && v.Name != "" {
		return map[string]string{"type": "function", "name": v.Name}, parallel, nil
	}
	if v.Name != "" {
		return nil, nil, errors.New("tool_choice.name 只适用于 type=tool")
	}
	switch v.Type {
	case "auto", "none":
		return v.Type, parallel, nil
	case "any":
		return "required", parallel, nil
	}
	return nil, nil, errors.New("Messages tool_choice 不支持")
}

// Only error bodies are rewritten here; successful model data is converted by
// compatOutput before it is written. Unwrap preserves deadlines and cancellation.
type messagesErrors struct {
	http.ResponseWriter
	status int
}

func (w *messagesErrors) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *messagesErrors) WriteHeader(status int) {
	w.status = status
	if status >= 400 {
		w.Header().Set("Content-Type", "application/json")
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *messagesErrors) Write(b []byte) (int, error) {
	if w.status < 400 {
		return w.ResponseWriter.Write(b)
	}
	kind := "api_error"
	switch w.status {
	case 400, 404, 408, 409, 413, 422:
		kind = "invalid_request_error"
	case 401:
		kind = "authentication_error"
	case 403:
		kind = "permission_error"
	case 429:
		kind = "rate_limit_error"
	case 503, 529:
		kind = "overloaded_error"
	}
	message := "网关或上游请求失败"
	var upstream struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(b, &upstream) == nil && upstream.Error.Message != "" {
		message = upstream.Error.Message
	}
	encoded := []byte(encode(map[string]any{"type": "error", "error": map[string]string{"type": kind, "message": message}, "request_id": w.Header().Get("X-Gateway-Request-Id")}))
	n, err := w.ResponseWriter.Write(encoded)
	if err == nil && n != len(encoded) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return 0, err
	}
	return len(b), nil
}
