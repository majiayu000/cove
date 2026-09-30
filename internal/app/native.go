package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// A beta is forwarded only on native Messages with an explicit model capability.
func validateMessagesBeta(header string, native bool, model SourceModel) error {
	if header == "" {
		return nil
	}
	if !native {
		return errors.New("Anthropic beta 功能不能跨协议转换")
	}
	for _, beta := range strings.Split(header, ",") {
		if !slices.Contains(model.Features, "anthropic_beta:"+strings.TrimSpace(beta)) {
			return errors.New("模型未声明支持此 Anthropic beta 功能")
		}
	}
	return nil
}

// Native envelopes remain raw. This converter handles only the declared text/function intersection.
func responsesToNative(body map[string]json.RawMessage, protocol string) (map[string]json.RawMessage, error) {
	supported := []string{"model", "input", "instructions", "stream", "store", "tools", "tool_choice", "parallel_tool_calls", "max_output_tokens", "temperature", "top_p", "reasoning", "text"}
	for k := range body {
		if !slices.Contains(supported, k) {
			return nil, fmt.Errorf("不能将 Responses 字段 %s 转换到 %s", k, protocol)
		}
	}
	if v := body["store"]; len(v) > 0 && string(v) != "false" && string(v) != "null" {
		return nil, errors.New("跨协议不能保留 provider 状态存储")
	}
	out := map[string]json.RawMessage{"model": body["model"], "stream": body["stream"]}
	if len(out["stream"]) == 0 {
		out["stream"] = json.RawMessage("false")
	}
	messages := []any{}
	system := []string{}
	var instruction string
	if len(body["instructions"]) > 0 {
		if json.Unmarshal(body["instructions"], &instruction) != nil {
			return nil, errors.New("instructions 必须是文本")
		}
		if instruction != "" {
			system = append(system, instruction)
		}
	}
	var items []map[string]json.RawMessage
	var simple string
	if json.Unmarshal(body["input"], &simple) == nil {
		items = []map[string]json.RawMessage{{"role": json.RawMessage(`"user"`), "content": json.RawMessage(encode(simple))}}
	} else if json.Unmarshal(body["input"], &items) != nil {
		return nil, errors.New("跨协议 input 结构无效")
	}
	for _, item := range items {
		var kind, role string
		_ = json.Unmarshal(item["type"], &kind)
		_ = json.Unmarshal(item["role"], &role)
		switch kind {
		case "", "message":
			var content string
			if json.Unmarshal(item["content"], &content) != nil {
				var parts []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if json.Unmarshal(item["content"], &parts) != nil {
					return nil, errors.New("仅支持转换文本内容")
				}
				for _, p := range parts {
					if p.Type != "input_text" && p.Type != "output_text" {
						return nil, errors.New("不可转换 opaque 或多模态内容")
					}
					content += p.Text
				}
			}
			if role == "system" || role == "developer" {
				if len(messages) > 0 {
					return nil, errors.New("不能转换中途动态 system/developer 作用域")
				}
				system = append(system, content)
			} else {
				if role != "user" && role != "assistant" {
					return nil, errors.New("消息角色无法转换")
				}
				messages = append(messages, map[string]any{"role": role, "content": content})
			}
		case "function_call":
			var callID, name, args string
			_ = json.Unmarshal(item["call_id"], &callID)
			_ = json.Unmarshal(item["name"], &name)
			_ = json.Unmarshal(item["arguments"], &args)
			if callID == "" || name == "" || !jsonObject([]byte(args)) {
				return nil, errors.New("函数调用参数无效")
			}
			if protocol == "chat_completions" {
				call := map[string]any{"id": callID, "type": "function", "function": map[string]string{"name": name, "arguments": args}}
				if len(messages) > 0 {
					last := messages[len(messages)-1].(map[string]any)
					if last["role"] == "assistant" {
						list, _ := last["tool_calls"].([]any)
						last["tool_calls"] = append(list, call)
						continue
					}
				}
				messages = append(messages, map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{call}})
			} else {
				block := map[string]any{"type": "tool_use", "id": callID, "name": name, "input": json.RawMessage(args)}
				messages = append(messages, map[string]any{"role": "assistant", "content": []any{block}})
			}
		case "function_call_output":
			var callID, value string
			_ = json.Unmarshal(item["call_id"], &callID)
			if json.Unmarshal(item["output"], &value) != nil || callID == "" {
				return nil, errors.New("工具结果必须为文本且有 call_id")
			}
			if protocol == "chat_completions" {
				messages = append(messages, map[string]any{"role": "tool", "tool_call_id": callID, "content": value})
			} else {
				messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "tool_use_id": callID, "content": value}}})
			}
		default:
			return nil, errors.New("不可跨协议重放签名 reasoning、custom 工具或其他 opaque 状态")
		}
	}
	if protocol == "chat_completions" {
		if len(system) > 0 {
			messages = append([]any{map[string]any{"role": "system", "content": strings.Join(system, "\n")}}, messages...)
		}
	} else if len(system) > 0 {
		out["system"] = json.RawMessage(encode(strings.Join(system, "\n")))
	}
	out["messages"] = json.RawMessage(encode(messages))
	if v := body["text"]; len(v) > 0 {
		if protocol != "chat_completions" {
			return nil, errors.New("此 Messages adapter 尚不能兑现结构化输出 schema")
		}
		var text struct {
			Format map[string]json.RawMessage `json:"format"`
		}
		if strictJSON(v, &text) != nil || text.Format == nil {
			return nil, errors.New("text.format 无法转换")
		}
		var kind string
		_ = json.Unmarshal(text.Format["type"], &kind)
		switch kind {
		case "text", "json_object":
			out["response_format"] = json.RawMessage(encode(map[string]string{"type": kind}))
		case "json_schema":
			delete(text.Format, "type")
			out["response_format"] = json.RawMessage(encode(map[string]any{"type": "json_schema", "json_schema": text.Format}))
		default:
			return nil, errors.New("text.format 无法转换")
		}
	}
	for _, k := range []string{"temperature", "top_p"} {
		if len(body[k]) > 0 {
			out[k] = body[k]
		}
	}
	if v := body["max_output_tokens"]; len(v) > 0 {
		if protocol == "messages" {
			out["max_tokens"] = v
		} else {
			out["max_completion_tokens"] = v
		}
	} else if protocol == "messages" {
		return nil, errors.New("原生 Messages 来源需要 max_output_tokens 或 max_tokens")
	}
	if len(body["reasoning"]) > 0 {
		if protocol != "chat_completions" {
			return nil, errors.New("reasoning effort 不能等价转换到 Messages")
		}
		var v struct {
			Effort string `json:"effort"`
		}
		if strictJSON(body["reasoning"], &v) != nil {
			return nil, errors.New("reasoning 无法转换")
		}
		out["reasoning_effort"] = json.RawMessage(encode(v.Effort))
	}
	var tools []map[string]json.RawMessage
	if len(body["tools"]) > 0 {
		if json.Unmarshal(body["tools"], &tools) != nil {
			return nil, errors.New("tools 无效")
		}
		converted := []any{}
		for _, tool := range tools {
			var kind string
			_ = json.Unmarshal(tool["type"], &kind)
			if kind != "function" {
				return nil, errors.New("跨协议只支持 function 工具")
			}
			function := map[string]json.RawMessage{}
			for _, k := range []string{"name", "description", "parameters", "strict"} {
				if v := tool[k]; len(v) > 0 {
					function[k] = v
				}
			}
			if protocol == "chat_completions" {
				converted = append(converted, map[string]any{"type": "function", "function": function})
			} else {
				var strict bool
				if json.Unmarshal(function["strict"], &strict) == nil && strict {
					return nil, errors.New("此 Messages 转换尚不能兑现 strict schema")
				}
				delete(function, "strict")
				function["input_schema"] = function["parameters"]
				delete(function, "parameters")
				converted = append(converted, function)
			}
		}
		out["tools"] = json.RawMessage(encode(converted))
	}
	if len(body["tool_choice"]) > 0 {
		var simple string
		var choice any
		if json.Unmarshal(body["tool_choice"], &simple) == nil {
			if protocol == "chat_completions" {
				choice = simple
			} else {
				if simple == "required" {
					simple = "any"
				}
				choice = map[string]any{"type": simple}
			}
		} else {
			var v struct {
				Type string `json:"type"`
				Name string `json:"name"`
			}
			if strictJSON(body["tool_choice"], &v) != nil || v.Type != "function" {
				return nil, errors.New("tool_choice 无法转换")
			}
			if protocol == "chat_completions" {
				choice = map[string]any{"type": "function", "function": map[string]string{"name": v.Name}}
			} else {
				choice = map[string]any{"type": "tool", "name": v.Name}
			}
		}
		out["tool_choice"] = json.RawMessage(encode(choice))
	}
	if len(body["parallel_tool_calls"]) > 0 {
		if protocol == "chat_completions" {
			out["parallel_tool_calls"] = body["parallel_tool_calls"]
		} else {
			var p bool
			if json.Unmarshal(body["parallel_tool_calls"], &p) != nil {
				return nil, errors.New("parallel_tool_calls 无效")
			}
			if !p {
				return nil, errors.New("此 Messages adapter 尚不能约束并行工具")
			}
		}
	}
	var stream bool
	_ = json.Unmarshal(out["stream"], &stream)
	if protocol == "chat_completions" && stream {
		out["stream_options"] = json.RawMessage(`{"include_usage":true}`)
	}
	return out, nil
}

type nativeOutput struct {
	choiceFinishes    map[int]string
	protocol          string
	response          map[string]any
	items             []map[string]any
	indices           map[int]int
	blocks            map[int]bool
	started, terminal bool
	finish            string
	size, max         int64
	usage             Usage
	emit              func([]byte) error
	aggregate         bool
}

func newNativeOutput(protocol string, max int64, aggregate bool) *nativeOutput {
	return &nativeOutput{protocol: protocol, choiceFinishes: map[int]string{}, response: map[string]any{"object": "response", "output": []any{}}, items: []map[string]any{}, indices: map[int]int{}, blocks: map[int]bool{}, max: max, aggregate: aggregate}
}
func (n *nativeOutput) event(kind string, fields map[string]any) error {
	if n.emit == nil {
		return nil
	}
	fields["type"] = kind
	return n.emit([]byte("event: " + kind + "\ndata: " + encode(fields) + "\n\n"))
}
func (n *nativeOutput) start(id, model string) error {
	if id != "" {
		n.response["id"] = id
	}
	if model != "" {
		n.response["model"] = model
	}
	if n.started {
		return nil
	}
	n.started = true
	v := map[string]any{}
	for k, value := range n.response {
		v[k] = value
	}
	v["status"] = "in_progress"
	return n.event("response.created", map[string]any{"response": v})
}
func (n *nativeOutput) add(index int, kind, callID, name, initial string) (int, error) {
	if oi, ok := n.indices[index]; ok {
		return oi, nil
	}
	oi := len(n.items)
	n.indices[index] = oi
	item := map[string]any{"type": kind}
	if kind == "function_call" {
		if callID == "" || name == "" {
			return 0, errors.New("工具起始信息缺少 ID 或名称")
		}
		item["call_id"] = callID
		item["name"] = name
		item["arguments"] = initial
	} else {
		item["role"] = "assistant"
		item["content"] = []any{map[string]any{"type": "output_text", "text": initial}}
	}
	n.items = append(n.items, item)
	if err := n.event("response.output_item.added", map[string]any{"output_index": oi, "item": item}); err != nil {
		return 0, err
	}
	return oi, nil
}
func (n *nativeOutput) delta(oi int, value string) error {
	n.size += int64(len(value))
	if n.size > n.max {
		return errors.New("跨协议聚合超出大小限制")
	}
	item := n.items[oi]
	kind := "response.output_text.delta"
	if item["type"] == "function_call" {
		item["arguments"] = item["arguments"].(string) + value
		kind = "response.function_call_arguments.delta"
	} else {
		part := item["content"].([]any)[0].(map[string]any)
		part["text"] = part["text"].(string) + value
	}
	return n.event(kind, map[string]any{"output_index": oi, "content_index": 0, "delta": value})
}
func (n *nativeOutput) complete() error {
	if n.terminal {
		return errors.New("重复终态")
	}
	if n.protocol == "chat_completions" && !n.aggregate {
		if len(n.choiceFinishes) == 0 {
			return errors.New("Chat流未出现结果")
		}
		n.finish = "stop"
		for _, finish := range n.choiceFinishes {
			if finish == "" {
				return errors.New("Chat某结果未结束")
			}
			if finish == "length" || finish == "content_filter" {
				n.finish = finish
			}
		}
	}
	if n.finish == "" {
		return errors.New("缺少生成终态")
	}
	n.terminal = true
	status := "completed"
	switch n.finish {
	case "stop", "tool_calls", "end_turn", "tool_use", "stop_sequence":
	case "length", "max_tokens":
		status = "incomplete"
		n.response["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
	case "content_filter", "refusal":
		status = "incomplete"
		n.response["incomplete_details"] = map[string]any{"reason": "content_filter"}
	default:
		return errors.New("无法转换结束原因")
	}
	for _, item := range n.items {
		if item["type"] == "function_call" && !jsonObject([]byte(item["arguments"].(string))) {
			return errors.New("工具终态不是完整 JSON 对象")
		}
	}
	n.response["status"] = status
	n.response["output"] = n.items
	n.response["usage"] = map[string]any{"input_tokens": n.usage.Input, "output_tokens": n.usage.Output, "input_tokens_details": map[string]any{"cached_tokens": n.usage.Cached, "cache_creation_tokens": n.usage.CacheCreation}, "output_tokens_details": map[string]any{"reasoning_tokens": n.usage.Reasoning}}
	return n.event("response."+status, map[string]any{"response": n.response})
}
func (n *nativeOutput) frame(frame []byte) error {
	data, kind := sseData(frame)
	if len(data) == 0 {
		return nil
	}
	if string(data) == "[DONE]" {
		if n.protocol != "chat_completions" {
			return errors.New("意外的终态标记")
		}
		return n.complete()
	}
	if n.terminal {
		return errors.New("终态后收到额外内容")
	}
	var v map[string]json.RawMessage
	if json.Unmarshal(data, &v) != nil {
		return errors.New("原生 SSE JSON 无效")
	}
	if len(v["error"]) > 0 {
		return errors.New("原生上游流失败")
	}
	if n.protocol == "chat_completions" {
		var chunk struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Usage   json.RawMessage `json:"usage"`
			Choices []struct {
				Index  int     `json:"index"`
				Finish *string `json:"finish_reason"`
				Delta  struct {
					Content *string `json:"content"`
					Refusal *string `json:"refusal"`
					Tools   []struct {
						Index    int    `json:"index"`
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal(data, &chunk) != nil {
			return errors.New("Chat chunk 无效")
		}
		if err := n.start(chunk.ID, chunk.Model); err != nil {
			return err
		}
		mergeNativeUsage(&n.usage, chunk.Usage, n.protocol)
		for _, c := range chunk.Choices {
			if _, exists := n.choiceFinishes[c.Index]; !exists {
				n.choiceFinishes[c.Index] = ""
			}
			if c.Finish != nil {
				n.choiceFinishes[c.Index] = *c.Finish
			}
			if n.aggregate && c.Index != 0 {
				return errors.New("跨协议仅支持一个生成结果")
			}
			if c.Finish != nil {
				n.finish = *c.Finish
			}
			if n.aggregate && c.Delta.Refusal != nil {
				return errors.New("流式 refusal 尚无无损转换")
			}
			if c.Delta.Content != nil && n.aggregate {
				oi, e := n.add(-1, "message", "", "", "")
				if e != nil {
					return e
				}
				if e = n.delta(oi, *c.Delta.Content); e != nil {
					return e
				}
			}
			for _, tool := range c.Delta.Tools {
				if !n.aggregate {
					continue
				}
				oi, exists := n.indices[tool.Index]
				if !exists {
					var e error
					oi, e = n.add(tool.Index, "function_call", tool.ID, tool.Function.Name, "")
					if e != nil {
						return e
					}
				}
				if e := n.delta(oi, tool.Function.Arguments); e != nil {
					return e
				}
			}
		}
		return nil
	}
	if kind == "" {
		_ = json.Unmarshal(v["type"], &kind)
	}
	switch kind {
	case "message_start":
		if n.started {
			return errors.New("重复 message_start")
		}
		var msg struct {
			ID    string          `json:"id"`
			Model string          `json:"model"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(v["message"], &msg) != nil {
			return errors.New("Messages start 无效")
		}
		mergeNativeUsage(&n.usage, msg.Usage, n.protocol)
		return n.start(msg.ID, msg.Model)
	case "content_block_start":
		if !n.started || n.finish != "" {
			return errors.New("内容开始不在活动消息中")
		}
		var blockIndex int
		if json.Unmarshal(v["index"], &blockIndex) != nil || blockIndex < 0 {
			return errors.New("内容 index 无效")
		}
		if _, exists := n.blocks[blockIndex]; exists {
			return errors.New("重复 content_block_start")
		}
		n.blocks[blockIndex] = true
		if !n.aggregate {
			return nil
		}
		var index int
		_ = json.Unmarshal(v["index"], &index)
		var b messageBlock
		if json.Unmarshal(v["content_block"], &b) != nil {
			return errors.New("Messages block 无效")
		}
		if b.Type == "text" {
			_, e := n.add(index, "message", "", "", b.Text)
			return e
		}
		if b.Type == "tool_use" {
			_, e := n.add(index, "function_call", b.ID, b.Name, "")
			return e
		}
		return errors.New("签名 thinking 或其他 opaque block 不可跨协议转换")
	case "content_block_delta":
		var blockIndex int
		if json.Unmarshal(v["index"], &blockIndex) != nil || !n.blocks[blockIndex] {
			return errors.New("内容增量不在活动 block 中")
		}
		if !n.aggregate {
			return nil
		}
		var index int
		_ = json.Unmarshal(v["index"], &index)
		oi, ok := n.indices[index]
		if !ok {
			return errors.New("内容增量早于 block start")
		}
		var d struct {
			Type    string `json:"type"`
			Text    string `json:"text"`
			Partial string `json:"partial_json"`
		}
		if json.Unmarshal(v["delta"], &d) != nil {
			return errors.New("Messages delta 无效")
		}
		if d.Type == "text_delta" {
			return n.delta(oi, d.Text)
		}
		if d.Type == "input_json_delta" {
			return n.delta(oi, d.Partial)
		}
		return errors.New("不可转换该 Messages delta")
	case "content_block_stop":
		var index int
		if json.Unmarshal(v["index"], &index) != nil || !n.blocks[index] {
			return errors.New("重复或未知 content_block_stop")
		}
		n.blocks[index] = false
	case "message_delta":
		if !n.started || n.finish != "" {
			return errors.New("message_delta 顺序无效")
		}
		for _, open := range n.blocks {
			if open {
				return errors.New("message_delta 早于内容结束")
			}
		}
		var d struct {
			StopReason string `json:"stop_reason"`
		}
		_ = json.Unmarshal(v["delta"], &d)
		n.finish = d.StopReason
		mergeNativeUsage(&n.usage, v["usage"], n.protocol)
	case "message_stop":
		for _, open := range n.blocks {
			if open {
				return errors.New("message_stop 早于内容结束")
			}
		}
		return n.complete()
	case "error":
		return errors.New("Messages 流错误")
	}
	return nil
}
func mergeNativeUsage(u *Usage, raw json.RawMessage, protocol string) {
	if protocol == "chat_completions" {
		var v struct {
			Input  *int64 `json:"prompt_tokens"`
			Output *int64 `json:"completion_tokens"`
			Cached struct {
				Value *int64 `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
			Reasoning struct {
				Value *int64 `json:"reasoning_tokens"`
			} `json:"completion_tokens_details"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return
		}
		if v.Input != nil {
			u.Input = v.Input
		}
		if v.Output != nil {
			u.Output = v.Output
		}
		if v.Cached.Value != nil {
			u.Cached = v.Cached.Value
		}
		if v.Reasoning.Value != nil {
			u.Reasoning = v.Reasoning.Value
		}
		return
	}
	var v struct {
		Input  *int64 `json:"input_tokens"`
		Output *int64 `json:"output_tokens"`
		Read   *int64 `json:"cache_read_input_tokens"`
		Create *int64 `json:"cache_creation_input_tokens"`
	}
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	if v.Input != nil {
		total := *v.Input
		if v.Read != nil {
			total += *v.Read
		}
		if v.Create != nil {
			total += *v.Create
			u.CacheCreation = v.Create
		}
		u.Input = &total
	}
	if v.Output != nil {
		u.Output = v.Output
	}
	if v.Read != nil {
		u.Cached = v.Read
	}
}
func nativeJSON(raw []byte, protocol string) ([]byte, error) {
	n := newNativeOutput(protocol, int64(len(raw))*2+1, true)
	if protocol == "chat_completions" {
		var v struct {
			ID      string `json:"id"`
			Model   string `json:"model"`
			Choices []struct {
				Message compatMessage `json:"message"`
				Finish  string        `json:"finish_reason"`
			} `json:"choices"`
			Usage json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(raw, &v) != nil || len(v.Choices) != 1 {
			return nil, errors.New("Chat response 结构无效")
		}
		n.start(v.ID, v.Model)
		n.finish = v.Choices[0].Finish
		m := v.Choices[0].Message
		if len(m.Content) > 0 && string(m.Content) != "null" {
			text, e := compatText(m.Content)
			if e != nil {
				return nil, e
			}
			n.add(-1, "message", "", "", text)
		}
		for i, t := range m.ToolCalls {
			if _, e := n.add(i, "function_call", t.ID, t.Function.Name, t.Function.Arguments); e != nil {
				return nil, e
			}
		}
		mergeNativeUsage(&n.usage, v.Usage, protocol)
	} else {
		var v struct {
			ID      string          `json:"id"`
			Model   string          `json:"model"`
			Content []messageBlock  `json:"content"`
			Stop    string          `json:"stop_reason"`
			Usage   json.RawMessage `json:"usage"`
		}
		if json.Unmarshal(raw, &v) != nil {
			return nil, errors.New("Messages response 无效")
		}
		n.start(v.ID, v.Model)
		n.finish = v.Stop
		for i, b := range v.Content {
			if b.Type == "text" {
				n.add(i, "message", "", "", b.Text)
			} else if b.Type == "tool_use" {
				if _, e := n.add(i, "function_call", b.ID, b.Name, string(b.Input)); e != nil {
					return nil, e
				}
			} else {
				return nil, fmt.Errorf("不能转换 Messages %s", b.Type)
			}
		}
		mergeNativeUsage(&n.usage, v.Usage, protocol)
	}
	if err := n.complete(); err != nil {
		return nil, err
	}
	return []byte(encode(n.response)), nil
}
func nativeID(prefix string, index int) string { return prefix + "_" + strconv.Itoa(index) }
func nativeMetadata(raw []byte, protocol string) ([]byte, error) {
	var v map[string]json.RawMessage
	if json.Unmarshal(raw, &v) != nil {
		return nil, errors.New("原生 JSON 无效")
	}
	var usage Usage
	mergeNativeUsage(&usage, v["usage"], protocol)
	var finish string
	if protocol == "messages" {
		_ = json.Unmarshal(v["stop_reason"], &finish)
	} else {
		var choices []struct {
			Finish string `json:"finish_reason"`
		}
		if json.Unmarshal(v["choices"], &choices) != nil || len(choices) == 0 {
			return nil, errors.New("原生响应缺少 choices")
		}
		finish = choices[0].Finish
		for _, c := range choices {
			if c.Finish == "" {
				return nil, errors.New("原生响应缺少终态")
			}
		}
	}
	if finish == "" {
		return nil, errors.New("原生响应缺少终态")
	}
	status := "completed"
	if finish == "length" || finish == "max_tokens" || finish == "content_filter" || finish == "refusal" {
		status = "incomplete"
	}
	return []byte(encode(map[string]any{"model": v["model"], "status": status, "usage": map[string]any{"input_tokens": usage.Input, "output_tokens": usage.Output, "input_tokens_details": map[string]any{"cached_tokens": usage.Cached, "cache_creation_tokens": usage.CacheCreation}, "output_tokens_details": map[string]any{"reasoning_tokens": usage.Reasoning}}})), nil
}

func candidateInput(clientRaw []byte, protocol string, src Source, sentModel string, max int64) (map[string]json.RawMessage, *compatOutput, bool, []byte, string, bool, error) {
	var envelope map[string]json.RawMessage
	if json.Unmarshal(clientRaw, &envelope) != nil {
		return nil, nil, false, nil, "", false, errors.New("请求 JSON 无效")
	}
	var originalModel string
	_ = json.Unmarshal(envelope["model"], &originalModel)
	raw := clientRaw
	if originalModel != sentModel {
		envelope["model"] = json.RawMessage(encode(sentModel))
		raw = []byte(encode(envelope))
	}
	native := src.Kind != "codex_subscription" && src.NativeProtocol != "" && src.NativeProtocol != "responses" && src.NativeProtocol == protocol
	body := envelope
	var output *compatOutput
	var err error
	if protocol == "gemini" && !native {
		body, _, err = geminiToResponses(raw)
		if err != nil {
			return nil, nil, false, nil, "", false, err
		}
		delete(body, "cove_stop_sequences")
		target := src.NativeProtocol
		if target == "" {
			target = "responses"
		}
		wire, _, e := geminiToNative(raw, target)
		if e != nil {
			return nil, nil, false, nil, "", false, e
		}
		raw = []byte(encode(wire))
	} else if src.NativeProtocol == "gemini" && !native {
		wire, _, e := nativeToGemini(raw, protocol)
		if e != nil {
			return nil, nil, false, nil, "", false, e
		}
		body, _, err = geminiToResponses([]byte(encode(wire)))
		delete(body, "cove_stop_sequences")
		if err != nil {
			return nil, nil, false, nil, "", false, err
		}
		if protocol != "responses" {
			clean := map[string]json.RawMessage{}
			for k, v := range envelope {
				if k != "stop" && k != "stop_sequences" {
					clean[k] = v
				}
			}
			_, output, err = compatInput([]byte(encode(clean)), protocol, src)
			if err != nil {
				return nil, nil, false, nil, "", false, err
			}
			output.max = max
		}
		raw = []byte(encode(wire))
	} else if protocol != "responses" && !native {
		body, output, err = compatInput(raw, protocol, src)
		if err != nil {
			return nil, nil, false, nil, "", false, err
		}
		output.max = max
		raw = []byte(encode(body))
	}
	var previous string
	var stream bool
	if native {
		if v := body["stream"]; len(v) > 0 && json.Unmarshal(v, &stream) != nil {
			err = errors.New("stream 无效")
		}
	} else {
		_, previous, stream, err = validateRequest(body, src)
	}
	if err != nil {
		return nil, nil, false, nil, "", false, err
	}
	if src.NativeProtocol != "" && src.NativeProtocol != "responses" && !native && protocol != "gemini" && src.NativeProtocol != "gemini" {
		converted, e := responsesToNative(body, src.NativeProtocol)
		if e != nil {
			return nil, nil, false, nil, "", false, e
		}
		raw = []byte(encode(converted))
	}
	return body, output, native, raw, previous, stream, nil
}
