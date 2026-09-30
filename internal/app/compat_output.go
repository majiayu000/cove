package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"strings"
	"time"
)

type responseOutput struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	Status     string          `json:"status"`
	Output     []responseItem  `json:"output"`
	Usage      json.RawMessage `json:"usage"`
	Incomplete struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}
type responseItem struct {
	ID        string `json:"id,omitempty"`
	Type      string `json:"type"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
}
type compatBlock struct {
	index, toolIndex   int
	kind, callID, name string
	sent               int
	digest             hash.Hash
}
type compatOutput struct {
	protocol, model, id           string
	stream, includeUsage, started bool
	created                       int64
	max                           int64
	written                       int64
	blocks                        map[string]*compatBlock
	ordered                       []*compatBlock
	tools                         int
	final                         json.RawMessage
	adjustments                   []string
	wireInput                     *int64
	completed                     map[int]responseItem
	completedBytes                int64
	outputObserved                bool
	lastOutputIndex               int
}

func (c *compatOutput) budget(b []byte) ([]byte, error) {
	c.written += int64(len(b))
	if c.written > c.max {
		return nil, errors.New("协议转换结果超出响应大小限制")
	}
	return b, nil
}
func (c *compatOutput) frame(event string, v any) []byte {
	if c.protocol == "messages" {
		return []byte("event: " + event + "\ndata: " + encode(v) + "\n\n")
	}
	return []byte("data: " + encode(v) + "\n\n")
}
func (c *compatOutput) chat(delta any, finish any) []byte {
	v := map[string]any{"id": c.id, "object": "chat.completion.chunk", "created": c.created, "model": c.model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish, "logprobs": nil}}}
	if c.includeUsage {
		v["usage"] = nil
	}
	return c.frame("", v)
}
func (c *compatOutput) start() []byte {
	if c.started {
		return nil
	}
	c.started = true
	c.created = time.Now().Unix()
	if c.id == "" {
		if c.protocol == "messages" {
			c.id = id("msg")
		} else {
			c.id = id("chatcmpl")
		}
	}
	if c.protocol == "chat_completions" {
		return c.chat(map[string]any{"role": "assistant", "content": ""}, nil)
	}
	return c.frame("message_start", map[string]any{"type": "message_start", "message": map[string]any{"id": c.id, "type": "message", "role": "assistant", "model": c.model, "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": c.wireInput, "output_tokens": 0}}})
}
func (c *compatOutput) block(outputIndex, contentIndex int, kind, callID, name string) (*compatBlock, []byte, error) {
	if outputIndex < 0 || contentIndex < 0 {
		return nil, nil, errors.New("上游输出索引无效")
	}
	key := fmt.Sprintf("%d:%d", outputIndex, contentIndex)
	if b := c.blocks[key]; b != nil {
		if b.kind != kind || kind == "function_call" && (b.callID != callID || b.name != name) {
			return nil, nil, errors.New("上游输出块身份不一致")
		}
		return b, nil, nil
	}
	b := &compatBlock{index: len(c.ordered), kind: kind, callID: callID, name: name, digest: sha256.New()}
	c.blocks[key] = b
	c.ordered = append(c.ordered, b)
	var block any
	if kind == "function_call" {
		if callID == "" || name == "" {
			return nil, nil, errors.New("上游工具调用缺少标识或名称")
		}
		b.toolIndex = c.tools
		c.tools++
		if c.protocol == "chat_completions" {
			return b, c.chat(map[string]any{"tool_calls": []any{map[string]any{"index": b.toolIndex, "id": callID, "type": "function", "function": map[string]string{"name": name, "arguments": ""}}}}, nil), nil
		}
		block = map[string]any{"type": "tool_use", "id": callID, "name": name, "input": map[string]any{}}
	} else {
		if c.protocol == "chat_completions" {
			return b, nil, nil
		}
		block = map[string]string{"type": "text", "text": ""}
	}
	return b, c.frame("content_block_start", map[string]any{"type": "content_block_start", "index": b.index, "content_block": block}), nil
}
func (c *compatOutput) delta(b *compatBlock, value string) []byte {
	if value == "" {
		return nil
	}
	b.sent += len(value)
	_, _ = b.digest.Write([]byte(value))
	if c.protocol == "chat_completions" {
		if b.kind == "function_call" {
			return c.chat(map[string]any{"tool_calls": []any{map[string]any{"index": b.toolIndex, "function": map[string]string{"arguments": value}}}}, nil)
		}
		key := "content"
		if b.kind == "refusal" {
			key = "refusal"
		}
		return c.chat(map[string]string{key: value}, nil)
	}
	delta := map[string]string{"type": "text_delta", "text": value}
	if b.kind == "function_call" {
		delta = map[string]string{"type": "input_json_delta", "partial_json": value}
	}
	return c.frame("content_block_delta", map[string]any{"type": "content_block_delta", "index": b.index, "delta": delta})
}

func (c *compatOutput) event(frame []byte) ([]byte, error) {
	data, kind := sseData(frame)
	if len(data) == 0 || string(data) == "[DONE]" {
		return nil, nil
	}
	var event struct {
		Type         string          `json:"type"`
		Response     json.RawMessage `json:"response"`
		OutputIndex  *int            `json:"output_index"`
		ContentIndex int             `json:"content_index"`
		Delta        string          `json:"delta"`
		Item         responseItem    `json:"item"`
	}
	if json.Unmarshal(data, &event) != nil {
		return nil, errors.New("上游 SSE 事件无法转换")
	}
	if kind == "" {
		kind = event.Type
	}
	switch kind {
	case "response.created", "response.in_progress":
		if !c.started {
			var response responseOutput
			if json.Unmarshal(event.Response, &response) == nil {
				if response.ID != "" {
					c.id = response.ID
				}
				if response.Model != "" {
					c.model = response.Model
				}
			}
		}
		if c.stream {
			return c.budget(c.start())
		}
		return nil, nil
	case "response.completed", "response.incomplete":
		if c.final != nil {
			return nil, errors.New("上游重复终结事件")
		}
		c.final = append([]byte(nil), event.Response...)
		return nil, nil
	case "response.failed", "error":
		return nil, errors.New("上游报告生成失败；未重试")
	}
	outputIndex := -1
	switch kind {
	case "response.output_item.added", "response.output_item.done", "response.function_call_arguments.delta", "response.output_text.delta", "response.refusal.delta":
		if event.OutputIndex == nil || *event.OutputIndex < 0 {
			return nil, errors.New("上游输出索引无效")
		}
		outputIndex = *event.OutputIndex
		c.outputObserved = true
		if outputIndex > c.lastOutputIndex {
			c.lastOutputIndex = outputIndex
		}
	}
	if kind == "response.output_item.done" {
		if c.final != nil {
			return nil, errors.New("上游输出项出现在终结事件之后")
		}
		if _, exists := c.completed[outputIndex]; exists {
			return nil, errors.New("上游重复完成输出项")
		}
		c.completedBytes += int64(len(data))
		if c.completedBytes > c.max {
			return nil, errors.New("上游完成输出项超出响应大小限制")
		}
		if c.completed == nil {
			c.completed = make(map[int]responseItem)
		}
		c.completed[outputIndex] = event.Item
		return nil, nil
	}
	if !c.stream {
		return nil, nil
	}
	out := []byte{}
	switch kind {
	case "response.output_item.added":
		if event.Item.Type != "function_call" {
			return nil, nil
		}
		out = append(out, c.start()...)
		b, start, err := c.block(outputIndex, 0, "function_call", event.Item.CallID, event.Item.Name)
		if err != nil {
			return nil, err
		}
		out = append(out, start...)
		out = append(out, c.delta(b, event.Item.Arguments)...)
	case "response.function_call_arguments.delta":
		b := c.blocks[fmt.Sprintf("%d:0", outputIndex)]
		if b == nil || b.kind != "function_call" {
			return nil, errors.New("工具参数出现在工具标识之前")
		}
		out = append(out, c.delta(b, event.Delta)...)
	case "response.output_text.delta", "response.refusal.delta":
		out = append(out, c.start()...)
		blockKind := "text"
		if kind == "response.refusal.delta" {
			blockKind = "refusal"
		}
		b, start, err := c.block(outputIndex, event.ContentIndex, blockKind, "", "")
		if err != nil {
			return nil, err
		}
		out = append(out, start...)
		out = append(out, c.delta(b, event.Delta)...)
	default:
		return nil, nil
	}
	return c.budget(out)
}

func compatUsage(raw json.RawMessage, protocol string) any {
	var u Usage
	mergeUsage(&u, raw)
	if protocol == "chat_completions" {
		if u.Input == nil && u.Output == nil {
			return nil
		}
		var total *int64
		if u.Input != nil && u.Output != nil {
			n := *u.Input + *u.Output
			total = &n
		}
		v := map[string]any{"prompt_tokens": u.Input, "completion_tokens": u.Output, "total_tokens": total}
		if u.Cached != nil {
			v["prompt_tokens_details"] = map[string]any{"cached_tokens": u.Cached}
		}
		if u.Reasoning != nil {
			v["completion_tokens_details"] = map[string]any{"reasoning_tokens": u.Reasoning}
		}
		return v
	}
	v := map[string]any{"input_tokens": u.Input, "output_tokens": u.Output}
	if u.Cached != nil {
		v["cache_read_input_tokens"] = u.Cached
		if u.Input != nil && *u.Input >= *u.Cached {
			v["input_tokens"] = *u.Input - *u.Cached
		} else {
			v["input_tokens"] = nil
		}
	}
	return v
}

func (c *compatOutput) finish(raw []byte) ([]byte, error) {
	var response responseOutput
	if json.Unmarshal(raw, &response) != nil {
		return nil, errors.New("缺少可转换的正式终结响应")
	}
	if response.Status != "completed" && response.Status != "incomplete" {
		return nil, errors.New("上游没有生成完成或输出上限终态")
	}
	// Completed-item events carry the actual output in Codex streams. The
	// terminal is still required for status and usage; deltas cannot invent it.
	if len(response.Output) == 0 && c.outputObserved {
		if len(c.completed) == 0 || c.lastOutputIndex != len(c.completed)-1 {
			return nil, errors.New("上游终态缺少完成的输出项")
		}
		response.Output = make([]responseItem, len(c.completed))
		for index := range response.Output {
			item, exists := c.completed[index]
			if !exists {
				return nil, errors.New("上游完成输出项索引不连续")
			}
			response.Output[index] = item
		}
	} else {
		for index, item := range c.completed {
			if index >= len(response.Output) || encode(item) != encode(response.Output[index]) {
				return nil, errors.New("上游终态与完成输出项不一致")
			}
		}
	}
	chatReason, messageReason := "stop", "end_turn"
	if response.Status == "incomplete" {
		switch response.Incomplete.Reason {
		case "max_output_tokens":
			chatReason, messageReason = "length", "max_tokens"
		case "content_filter":
			chatReason, messageReason = "content_filter", "refusal"
		default:
			return nil, errors.New("上游响应不完整，无法映射结束原因")
		}
	}
	if !c.started {
		if response.ID != "" {
			c.id = response.ID
		}
		if response.Model != "" {
			c.model = response.Model
		}
	}
	var wire []byte
	if c.stream {
		wire = append(wire, c.start()...)
	} else {
		c.start()
	}
	content, calls := []any{}, []any{}
	var text, refusal strings.Builder
	finalBlocks := 0
	appendValue := func(oi, ci int, kind, value, callID, name string) error {
		finalBlocks++
		if !c.stream {
			return nil
		}
		b, start, err := c.block(oi, ci, kind, callID, name)
		if err != nil {
			return err
		}
		if b.sent > len(value) {
			return errors.New("上游终态与流式输出长度不一致")
		}
		prefix := sha256.Sum256([]byte(value[:b.sent]))
		if !bytes.Equal(prefix[:], b.digest.Sum(nil)) {
			return errors.New("上游终态与已发送内容不一致")
		}
		wire = append(wire, start...)
		wire = append(wire, c.delta(b, value[b.sent:])...)
		return nil
	}
	for oi, item := range response.Output {
		switch item.Type {
		case "reasoning": // No signed/thinking representation is invented for another API.
			continue
		case "function_call":
			if item.CallID == "" || item.Name == "" || !jsonObject([]byte(item.Arguments)) {
				return nil, errors.New("上游工具调用不是有效对象参数")
			}
			calls = append(calls, map[string]any{"id": item.CallID, "type": "function", "function": map[string]string{"name": item.Name, "arguments": item.Arguments}})
			content = append(content, map[string]any{"type": "tool_use", "id": item.CallID, "name": item.Name, "input": json.RawMessage(item.Arguments)})
			if err := appendValue(oi, 0, "function_call", item.Arguments, item.CallID, item.Name); err != nil {
				return nil, err
			}
		case "message":
			for ci, part := range item.Content {
				kind, value := "text", part.Text
				if part.Type == "refusal" {
					kind, value = "refusal", part.Refusal
					refusal.WriteString(value)
					messageReason = "refusal"
				} else if part.Type == "output_text" {
					text.WriteString(value)
				} else {
					return nil, errors.New("上游输出含无法转换的内容类型")
				}
				content = append(content, map[string]string{"type": "text", "text": value})
				if err := appendValue(oi, ci, kind, value, "", ""); err != nil {
					return nil, err
				}
			}
		default:
			return nil, errors.New("上游输出含无法转换的工具或模态")
		}
	}
	if len(calls) > 0 && response.Status == "completed" {
		chatReason, messageReason = "tool_calls", "tool_use"
	}
	usage := compatUsage(response.Usage, c.protocol)
	if c.protocol == "messages" {
		if c.stream {
			if c.wireInput == nil {
				return nil, errors.New("Messages 转换流缺少固定 tokenizer 估算")
			}
			estimate, err := estimateTokens(text.String() + encode(calls))
			if err != nil {
				return nil, err
			}
			usage = map[string]any{"output_tokens": estimate}
		} else {
			var actual Usage
			mergeUsage(&actual, response.Usage)
			if actual.Input == nil || actual.Output == nil {
				return nil, errors.New("Messages 非流式响应缺少实际 token 计数；请选择原生来源")
			}
		}
	}
	if c.stream {
		if finalBlocks != len(c.ordered) {
			return nil, errors.New("上游终态缺少已发送的内容块")
		}
		if c.protocol == "chat_completions" {
			wire = append(wire, c.chat(map[string]any{}, chatReason)...)
			if c.includeUsage {
				wire = append(wire, c.frame("", map[string]any{"id": c.id, "object": "chat.completion.chunk", "created": c.created, "model": c.model, "choices": []any{}, "usage": usage})...)
			}
			wire = append(wire, []byte("data: [DONE]\n\n")...)
		} else {
			for _, b := range c.ordered {
				wire = append(wire, c.frame("content_block_stop", map[string]any{"type": "content_block_stop", "index": b.index})...)
			}
			wire = append(wire, c.frame("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": messageReason, "stop_sequence": nil}, "usage": usage})...)
			wire = append(wire, c.frame("message_stop", map[string]string{"type": "message_stop"})...)
		}
		return c.budget(wire)
	}
	var result any
	if c.protocol == "messages" {
		result = map[string]any{"id": c.id, "type": "message", "role": "assistant", "model": c.model, "content": content, "stop_reason": messageReason, "stop_sequence": nil, "usage": usage}
	} else {
		message := map[string]any{"role": "assistant", "content": nil, "refusal": nil}
		if text.Len() > 0 || len(calls) == 0 && refusal.Len() == 0 {
			message["content"] = text.String()
		}
		if refusal.Len() > 0 {
			message["refusal"] = refusal.String()
		}
		if len(calls) > 0 {
			message["tool_calls"] = calls
		}
		result = map[string]any{"id": c.id, "object": "chat.completion", "created": c.created, "model": c.model, "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": chatReason, "logprobs": nil}}, "usage": usage}
	}
	return c.budget([]byte(encode(result)))
}

func (c *compatOutput) streamError(requestID string) []byte {
	message := "上游流或协议转换失败，未重试；请用 request_id 查询详情"
	if c.protocol == "messages" {
		return c.frame("error", map[string]any{"type": "error", "error": map[string]string{"type": "api_error", "message": message}, "request_id": requestID})
	}
	return c.frame("", map[string]any{"error": map[string]string{"type": "gateway_error", "message": message}, "request_id": requestID})
}
