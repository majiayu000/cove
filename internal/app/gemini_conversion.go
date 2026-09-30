package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
)

// Conversion errors distinguish malformed histories (400) from fields whose
// semantics the destination cannot carry (422). Callers must preserve this.
type GeminiConversionError struct {
	Status         int
	Field, Message string
}

func (e *GeminiConversionError) Error() string { return e.Field + ": " + e.Message }
func geminiConversionStatus(err error) (int, string) {
	var conversion *GeminiConversionError
	if errors.As(err, &conversion) {
		return conversion.Status, conversion.Field
	}
	return 422, ""
}
func geminiConversionError(field, message string) error {
	return &GeminiConversionError{422, field, message}
}
func geminiHistoryError(field, message string) error {
	return &GeminiConversionError{400, field, message}
}
func geminiFields(value map[string]json.RawMessage, path string, names ...string) error {
	for key := range value {
		if !slices.Contains(names, key) {
			return geminiConversionError(path+"."+key, "目标协议没有同义字段")
		}
	}
	return nil
}
func geminiObject(raw []byte, path string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return nil, geminiHistoryError(path, "需要 JSON 对象")
	}
	return value, nil
}
func geminiText(raw json.RawMessage, path string) (string, error) {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return "", geminiHistoryError(path, "需要字符串")
	}
	return value, nil
}
func geminiStrings(raw json.RawMessage, path string) ([]string, error) {
	var value []string
	if json.Unmarshal(raw, &value) != nil {
		return nil, geminiHistoryError(path, "需要字符串数组")
	}
	return value, nil
}

// model/stream are Cove's explicit envelope fields, inserted from the Gemini
// URL by ingress. They are removed again before the native Gemini POST.
func geminiToResponses(raw []byte) (map[string]json.RawMessage, []string, error) {
	body, err := geminiObject(raw, "request")
	if err != nil {
		return nil, nil, err
	}
	if err = geminiFields(body, "request", "model", "stream", "contents", "systemInstruction", "generationConfig", "tools", "toolConfig"); err != nil {
		return nil, nil, err
	}
	model, err := geminiText(body["model"], "model")
	if err != nil || model == "" {
		return nil, nil, geminiHistoryError("model", "路径 model 必须注入转换 envelope")
	}
	out := map[string]json.RawMessage{"model": body["model"], "store": json.RawMessage("false")}
	if v, ok := body["stream"]; ok {
		var stream bool
		if json.Unmarshal(v, &stream) != nil {
			return nil, nil, geminiHistoryError("stream", "需要布尔值")
		}
		out["stream"] = v
	} else {
		out["stream"] = json.RawMessage("false")
	}
	items := []any{}
	if value, ok := body["systemInstruction"]; ok {
		content, e := geminiObject(value, "systemInstruction")
		if e != nil {
			return nil, nil, e
		}
		if e = geminiFields(content, "systemInstruction", "role", "parts"); e != nil {
			return nil, nil, e
		}
		parts, e := geminiPlainParts(content["parts"], "systemInstruction.parts")
		if e != nil {
			return nil, nil, e
		}
		converted := []any{}
		for _, text := range parts {
			converted = append(converted, map[string]any{"type": "input_text", "text": text})
		}
		items = append(items, map[string]any{"role": "system", "content": converted})
	}
	var contents []map[string]json.RawMessage
	if json.Unmarshal(body["contents"], &contents) != nil || len(contents) == 0 {
		return nil, nil, geminiHistoryError("contents", "需要非空 Content 数组")
	}
	pending := map[string]string{}
	byName := map[string][]string{}
	used := map[string]bool{}
	ordinal := 0
	for ci, content := range contents {
		path := fmt.Sprintf("contents[%d]", ci)
		if err = geminiFields(content, path, "role", "parts"); err != nil {
			return nil, nil, err
		}
		role := "user"
		if v, ok := content["role"]; ok {
			role, err = geminiText(v, path+".role")
			if err != nil {
				return nil, nil, err
			}
			if role == "" {
				role = "user"
			}
		}
		if role == "model" {
			role = "assistant"
		} else if role != "user" {
			return nil, nil, geminiConversionError(path+".role", "仅支持 user/model")
		}
		var parts []map[string]json.RawMessage
		if json.Unmarshal(content["parts"], &parts) != nil || len(parts) == 0 {
			return nil, nil, geminiHistoryError(path+".parts", "需要非空 Part 数组")
		}
		texts := []any{}
		flush := func() {
			if len(texts) > 0 {
				items = append(items, map[string]any{"role": role, "content": texts})
				texts = []any{}
			}
		}
		for pi, part := range parts {
			partPath := fmt.Sprintf("%s.parts[%d]", path, pi)
			if err = geminiFields(part, partPath, "text", "functionCall", "functionResponse"); err != nil {
				return nil, nil, err
			}
			if len(part) != 1 {
				return nil, nil, geminiHistoryError(partPath, "文本/函数调用/函数结果必须明确唯一")
			}
			if value, ok := part["text"]; ok {
				text, e := geminiText(value, partPath+".text")
				if e != nil {
					return nil, nil, e
				}
				kind := "input_text"
				if role == "assistant" {
					kind = "output_text"
				}
				texts = append(texts, map[string]any{"type": kind, "text": text})
				continue
			}
			flush()
			if value, ok := part["functionCall"]; ok {
				if role != "assistant" {
					return nil, nil, geminiHistoryError(partPath+".functionCall", "必须属于 model 内容")
				}
				function, e := geminiObject(value, partPath+".functionCall")
				if e != nil {
					return nil, nil, e
				}
				if e = geminiFields(function, partPath+".functionCall", "name", "args", "id"); e != nil {
					return nil, nil, e
				}
				name, e := geminiText(function["name"], partPath+".functionCall.name")
				if e != nil || name == "" {
					return nil, nil, geminiHistoryError(partPath+".functionCall.name", "需要非空函数名")
				}
				args := function["args"]
				if len(args) == 0 {
					args = json.RawMessage("{}")
				}
				if !jsonObject(args) {
					return nil, nil, geminiHistoryError(partPath+".functionCall.args", "需要对象参数")
				}
				callID := ""
				if v, ok := function["id"]; ok {
					callID, e = geminiText(v, partPath+".functionCall.id")
					if e != nil {
						return nil, nil, e
					}
				}
				if callID == "" {
					canonical, e := canonicalCacheJSON(value)
					if e != nil {
						return nil, nil, geminiHistoryError(partPath, e.Error())
					}
					callID = "call_cove_history_" + digest(string(canonical) + fmt.Sprintf("/%d", ordinal))[:24]
				}
				ordinal++
				if used[callID] {
					return nil, nil, geminiHistoryError(partPath+".functionCall.id", "重复调用 ID")
				}
				used[callID] = true
				pending[callID] = name
				byName[name] = append(byName[name], callID)
				items = append(items, map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": string(args)})
				continue
			}
			function, e := geminiObject(part["functionResponse"], partPath+".functionResponse")
			if e != nil {
				return nil, nil, e
			}
			if e = geminiFields(function, partPath+".functionResponse", "name", "response", "id"); e != nil {
				return nil, nil, e
			}
			if role != "user" {
				return nil, nil, geminiHistoryError(partPath+".functionResponse", "必须属于 user 内容")
			}
			name, e := geminiText(function["name"], partPath+".functionResponse.name")
			if e != nil || name == "" {
				return nil, nil, geminiHistoryError(partPath+".functionResponse.name", "需要非空函数名")
			}
			callID := ""
			if v, ok := function["id"]; ok {
				callID, e = geminiText(v, partPath+".functionResponse.id")
				if e != nil {
					return nil, nil, e
				}
			}
			if callID == "" {
				if len(byName[name]) != 1 {
					return nil, nil, geminiHistoryError(partPath+".functionResponse.id", "并行同名结果不能按 name 猜测，需要明确 ID")
				}
				callID = byName[name][0]
			}
			if pending[callID] != name {
				return nil, nil, geminiHistoryError(partPath+".functionResponse.id", "结果没有匹配的调用 ID/name")
			}
			if !jsonObject(function["response"]) {
				return nil, nil, geminiHistoryError(partPath+".functionResponse.response", "需要对象结果")
			}
			delete(pending, callID)
			index := slices.Index(byName[name], callID)
			byName[name] = slices.Delete(byName[name], index, index+1)
			items = append(items, map[string]any{"type": "function_call_output", "call_id": callID, "output": string(function["response"])})
		}
		flush()
	}
	out["input"] = json.RawMessage(encode(items))
	if configRaw, ok := body["generationConfig"]; ok {
		config, e := geminiObject(configRaw, "generationConfig")
		if e != nil {
			return nil, nil, e
		}
		if e = geminiFields(config, "generationConfig", "maxOutputTokens", "temperature", "topP", "stopSequences", "candidateCount"); e != nil {
			return nil, nil, e
		}
		if n, ok := config["candidateCount"]; ok {
			var count int
			if json.Unmarshal(n, &count) != nil || count != 1 {
				return nil, nil, geminiConversionError("generationConfig.candidateCount", "转换仅有一个候选的同义合同")
			}
		}
		for from, to := range map[string]string{"maxOutputTokens": "max_output_tokens", "temperature": "temperature", "topP": "top_p", "stopSequences": "cove_stop_sequences"} {
			if value, ok := config[from]; ok {
				out[to] = value
			}
		}
	}
	if value, ok := body["tools"]; ok {
		var tools []map[string]json.RawMessage
		if json.Unmarshal(value, &tools) != nil {
			return nil, nil, geminiHistoryError("tools", "需要数组")
		}
		declarations := []any{}
		for ti, tool := range tools {
			path := fmt.Sprintf("tools[%d]", ti)
			if err = geminiFields(tool, path, "functionDeclarations"); err != nil {
				return nil, nil, err
			}
			var functions []map[string]json.RawMessage
			if json.Unmarshal(tool["functionDeclarations"], &functions) != nil {
				return nil, nil, geminiHistoryError(path+".functionDeclarations", "需要数组")
			}
			for fi, function := range functions {
				fp := fmt.Sprintf("%s.functionDeclarations[%d]", path, fi)
				if err = geminiFields(function, fp, "name", "description", "parameters", "parametersJsonSchema"); err != nil {
					return nil, nil, err
				}
				name, e := geminiText(function["name"], fp+".name")
				if e != nil || name == "" {
					return nil, nil, geminiHistoryError(fp+".name", "需要非空名称")
				}
				declaration := map[string]any{"type": "function", "name": name}
				if description, ok := function["description"]; ok {
					declaration["description"] = description
				}
				if schema, ok := function["parametersJsonSchema"]; ok {
					if _, also := function["parameters"]; also {
						return nil, nil, geminiHistoryError(fp, "不能同时声明两种参数 schema")
					}
					if !jsonObject(schema) {
						return nil, nil, geminiHistoryError(fp+".parametersJsonSchema", "需要 JSON Schema 对象")
					}
					declaration["parameters"] = schema
				} else if schema, ok := function["parameters"]; ok {
					converted, e := geminiSchemaToJSON(schema, fp+".parameters")
					if e != nil {
						return nil, nil, e
					}
					declaration["parameters"] = converted
				} else {
					declaration["parameters"] = map[string]any{"type": "object", "properties": map[string]any{}}
				}
				declarations = append(declarations, declaration)
			}
		}
		out["tools"] = json.RawMessage(encode(declarations))
	}
	if value, ok := body["toolConfig"]; ok {
		config, e := geminiObject(value, "toolConfig")
		if e != nil {
			return nil, nil, e
		}
		if e = geminiFields(config, "toolConfig", "functionCallingConfig"); e != nil {
			return nil, nil, e
		}
		function, e := geminiObject(config["functionCallingConfig"], "toolConfig.functionCallingConfig")
		if e != nil {
			return nil, nil, e
		}
		if e = geminiFields(function, "toolConfig.functionCallingConfig", "mode", "allowedFunctionNames"); e != nil {
			return nil, nil, e
		}
		mode, e := geminiText(function["mode"], "toolConfig.functionCallingConfig.mode")
		if e != nil {
			return nil, nil, e
		}
		choice := map[string]string{"AUTO": "auto", "NONE": "none", "ANY": "required"}[mode]
		if choice == "" {
			return nil, nil, geminiConversionError("toolConfig.functionCallingConfig.mode", "此模式尚无同义映射")
		}
		out["tool_choice"] = json.RawMessage(encode(choice))
		if namesRaw, ok := function["allowedFunctionNames"]; ok {
			names, e := geminiStrings(namesRaw, "toolConfig.functionCallingConfig.allowedFunctionNames")
			if e != nil {
				return nil, nil, e
			}
			if mode != "ANY" || len(names) != 1 {
				return nil, nil, geminiConversionError("toolConfig.functionCallingConfig.allowedFunctionNames", "仅 ANY 的单函数选择可无损映射")
			}
			out["tool_choice"] = json.RawMessage(encode(map[string]string{"type": "function", "name": names[0]}))
		}
	}
	return out, nil, nil
}

func geminiPlainParts(raw json.RawMessage, path string) ([]string, error) {
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return nil, geminiHistoryError(path, "需要非空文本 Part 数组")
	}
	out := []string{}
	for i, part := range parts {
		fp := fmt.Sprintf("%s[%d]", path, i)
		if err := geminiFields(part, fp, "text"); err != nil {
			return nil, err
		}
		text, err := geminiText(part["text"], fp+".text")
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, nil
}
func geminiSchemaToJSON(raw json.RawMessage, path string) (json.RawMessage, error) {
	schema, err := geminiObject(raw, path)
	if err != nil {
		return nil, err
	}
	if err = geminiFields(schema, path, "type", "format", "title", "description", "enum", "properties", "required", "items", "minItems", "maxItems", "minimum", "maximum", "minLength", "maxLength", "pattern", "nullable", "anyOf"); err != nil {
		return nil, err
	}
	if typ, ok := schema["type"]; ok {
		kind, e := geminiText(typ, path+".type")
		if e != nil {
			return nil, e
		}
		kind = strings.ToLower(kind)
		if !slices.Contains([]string{"object", "array", "string", "number", "integer", "boolean", "null"}, kind) {
			return nil, geminiConversionError(path+".type", "未知 Schema type")
		}
		schema["type"] = json.RawMessage(encode(kind))
	}
	if raw, ok := schema["nullable"]; ok {
		var nullable bool
		if json.Unmarshal(raw, &nullable) != nil {
			return nil, geminiHistoryError(path+".nullable", "需要布尔值")
		}
		delete(schema, "nullable")
		if nullable {
			var kind string
			if json.Unmarshal(schema["type"], &kind) != nil {
				return nil, geminiConversionError(path+".nullable", "缺少可确定的 type")
			}
			schema["type"] = json.RawMessage(encode([]string{kind, "null"}))
		}
	}
	if value, ok := schema["properties"]; ok {
		properties, e := geminiObject(value, path+".properties")
		if e != nil {
			return nil, e
		}
		for name, property := range properties {
			properties[name], e = geminiSchemaToJSON(property, path+".properties."+name)
			if e != nil {
				return nil, e
			}
		}
		schema["properties"] = json.RawMessage(encode(properties))
	}
	if value, ok := schema["items"]; ok {
		schema["items"], err = geminiSchemaToJSON(value, path+".items")
		if err != nil {
			return nil, err
		}
	}
	if value, ok := schema["anyOf"]; ok {
		var alternatives []json.RawMessage
		if json.Unmarshal(value, &alternatives) != nil {
			return nil, geminiHistoryError(path+".anyOf", "需要数组")
		}
		for i, alternative := range alternatives {
			alternatives[i], err = geminiSchemaToJSON(alternative, fmt.Sprintf("%s.anyOf[%d]", path, i))
			if err != nil {
				return nil, err
			}
		}
		schema["anyOf"] = json.RawMessage(encode(alternatives))
	}
	return json.RawMessage(encode(schema)), nil
}

func responsesToGemini(body map[string]json.RawMessage) (map[string]json.RawMessage, []string, error) {
	if err := geminiFields(body, "request", "model", "input", "instructions", "stream", "store", "tools", "tool_choice", "parallel_tool_calls", "max_output_tokens", "temperature", "top_p", "cove_stop_sequences"); err != nil {
		return nil, nil, err
	}
	if value, ok := body["store"]; ok && string(value) != "false" && string(value) != "null" {
		return nil, nil, geminiConversionError("store", "Gemini 无 Responses 状态资源合同")
	}
	if value, ok := body["parallel_tool_calls"]; ok {
		var allowed bool
		if json.Unmarshal(value, &allowed) != nil {
			return nil, nil, geminiHistoryError("parallel_tool_calls", "需要布尔值")
		}
		if !allowed {
			return nil, nil, geminiConversionError("parallel_tool_calls", "Gemini 无禁止并行工具的同义设置")
		}
	}
	out := map[string]json.RawMessage{"model": body["model"], "stream": body["stream"]}
	if len(out["stream"]) == 0 {
		out["stream"] = json.RawMessage("false")
	}
	contents := []map[string]any{}
	system := []string{}
	adjustments := []string{}
	if value, ok := body["instructions"]; ok {
		text, e := geminiText(value, "instructions")
		if e != nil {
			return nil, nil, e
		}
		system = append(system, text)
	}
	var items []map[string]json.RawMessage
	var text string
	if json.Unmarshal(body["input"], &text) == nil {
		items = []map[string]json.RawMessage{{"role": json.RawMessage(`"user"`), "content": body["input"]}}
	} else if json.Unmarshal(body["input"], &items) != nil {
		return nil, nil, geminiHistoryError("input", "需要文本或输入项数组")
	}
	add := func(role string, part any) {
		if len(contents) > 0 && contents[len(contents)-1]["role"] == role {
			last := contents[len(contents)-1]
			last["parts"] = append(last["parts"].([]any), part)
		} else {
			contents = append(contents, map[string]any{"role": role, "parts": []any{part}})
		}
	}
	pending := map[string]string{}
	used := map[string]bool{}
	for i, item := range items {
		path := fmt.Sprintf("input[%d]", i)
		kind := ""
		if value, ok := item["type"]; ok {
			var e error
			kind, e = geminiText(value, path+".type")
			if e != nil {
				return nil, nil, e
			}
		}
		switch kind {
		case "", "message":
			if err := geminiFields(item, path, "type", "role", "content"); err != nil {
				return nil, nil, err
			}
			role, e := geminiText(item["role"], path+".role")
			if e != nil {
				return nil, nil, e
			}
			texts, e := responsesTextParts(item["content"], path+".content")
			if e != nil {
				return nil, nil, e
			}
			if role == "system" || role == "developer" {
				if len(contents) > 0 {
					return nil, nil, geminiConversionError(path+".role", "不能保留中途 system/developer 的作用域")
				}
				system = append(system, strings.Join(texts, ""))
				continue
			}
			if role == "assistant" {
				role = "model"
			} else if role != "user" {
				return nil, nil, geminiConversionError(path+".role", "此角色无法同义表示")
			}
			for _, text := range texts {
				add(role, map[string]string{"text": text})
			}
		case "function_call":
			if err := geminiFields(item, path, "type", "id", "status", "call_id", "name", "arguments"); err != nil {
				return nil, nil, err
			}
			callID, e := geminiText(item["call_id"], path+".call_id")
			if e != nil || callID == "" {
				return nil, nil, geminiHistoryError(path+".call_id", "需要调用 ID")
			}
			if used[callID] {
				return nil, nil, geminiHistoryError(path+".call_id", "重复调用 ID")
			}
			name, e := geminiText(item["name"], path+".name")
			if e != nil || name == "" {
				return nil, nil, geminiHistoryError(path+".name", "需要函数名")
			}
			args, e := geminiText(item["arguments"], path+".arguments")
			if e != nil || !jsonObject([]byte(args)) {
				return nil, nil, geminiHistoryError(path+".arguments", "需要完整对象 JSON")
			}
			used[callID] = true
			pending[callID] = name
			add("model", map[string]any{"functionCall": map[string]any{"id": callID, "name": name, "args": json.RawMessage(args)}})
		case "function_call_output":
			if err := geminiFields(item, path, "type", "call_id", "output"); err != nil {
				return nil, nil, err
			}
			callID, e := geminiText(item["call_id"], path+".call_id")
			if e != nil || pending[callID] == "" {
				return nil, nil, geminiHistoryError(path+".call_id", "完整历史中缺少匹配的工具调用")
			}
			value, e := geminiText(item["output"], path+".output")
			if e != nil {
				return nil, nil, e
			}
			result := any(map[string]string{"output": value})
			if jsonObject([]byte(value)) {
				result = json.RawMessage(value)
			}
			add("user", map[string]any{"functionResponse": map[string]any{"id": callID, "name": pending[callID], "response": result}})
			delete(pending, callID)
		default:
			return nil, nil, geminiConversionError(path+".type", "opaque、服务端工具或扩展输入没有 Gemini 同义载体")
		}
	}
	if len(system) > 0 {
		if len(system) > 1 {
			adjustments = append(adjustments, "system_roles_merged")
		}
		out["systemInstruction"] = json.RawMessage(encode(map[string]any{"parts": []any{map[string]string{"text": strings.Join(system, "\n\n")}}}))
	}
	if len(contents) == 0 {
		return nil, nil, geminiHistoryError("input", "缺少用户或模型内容")
	}
	out["contents"] = json.RawMessage(encode(contents))
	config := map[string]json.RawMessage{}
	for from, to := range map[string]string{"max_output_tokens": "maxOutputTokens", "temperature": "temperature", "top_p": "topP", "cove_stop_sequences": "stopSequences"} {
		if value, ok := body[from]; ok {
			config[to] = value
		}
	}
	if len(config) > 0 {
		out["generationConfig"] = json.RawMessage(encode(config))
	}
	if value, ok := body["tools"]; ok {
		var functions []map[string]json.RawMessage
		if json.Unmarshal(value, &functions) != nil {
			return nil, nil, geminiHistoryError("tools", "需要数组")
		}
		declarations := []any{}
		for i, function := range functions {
			path := fmt.Sprintf("tools[%d]", i)
			if err := geminiFields(function, path, "type", "name", "description", "parameters", "strict"); err != nil {
				return nil, nil, err
			}
			kind, e := geminiText(function["type"], path+".type")
			if e != nil || kind != "function" {
				return nil, nil, geminiConversionError(path+".type", "只支持客户端 function")
			}
			if strict, ok := function["strict"]; ok && string(strict) != "false" {
				return nil, nil, geminiConversionError(path+".strict", "没有保证同义的严格参数模式")
			}
			declaration := map[string]json.RawMessage{"name": function["name"]}
			if value, ok := function["description"]; ok {
				declaration["description"] = value
			}
			if value, ok := function["parameters"]; ok {
				if !jsonObject(value) {
					return nil, nil, geminiHistoryError(path+".parameters", "需要 JSON Schema 对象")
				}
				declaration["parametersJsonSchema"] = value
			}
			declarations = append(declarations, declaration)
		}
		out["tools"] = json.RawMessage(encode([]any{map[string]any{"functionDeclarations": declarations}}))
	}
	if value, ok := body["tool_choice"]; ok {
		var choice string
		config := map[string]any{}
		if json.Unmarshal(value, &choice) == nil {
			mode := map[string]string{"auto": "AUTO", "none": "NONE", "required": "ANY"}[choice]
			if mode == "" {
				return nil, nil, geminiConversionError("tool_choice", "无同义模式")
			}
			config["mode"] = mode
		} else {
			function, e := geminiObject(value, "tool_choice")
			if e != nil {
				return nil, nil, e
			}
			if e = geminiFields(function, "tool_choice", "type", "name"); e != nil {
				return nil, nil, e
			}
			kind, e := geminiText(function["type"], "tool_choice.type")
			if e != nil || kind != "function" {
				return nil, nil, geminiConversionError("tool_choice.type", "无同义模式")
			}
			name, e := geminiText(function["name"], "tool_choice.name")
			if e != nil {
				return nil, nil, e
			}
			config["mode"] = "ANY"
			config["allowedFunctionNames"] = []string{name}
		}
		out["toolConfig"] = json.RawMessage(encode(map[string]any{"functionCallingConfig": config}))
	}
	return out, adjustments, nil
}
func responsesTextParts(raw json.RawMessage, path string) ([]string, error) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []string{text}, nil
	}
	var parts []map[string]json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return nil, geminiHistoryError(path, "需要文本或文本内容数组")
	}
	out := []string{}
	for i, part := range parts {
		fp := fmt.Sprintf("%s[%d]", path, i)
		if err := geminiFields(part, fp, "type", "text"); err != nil {
			return nil, err
		}
		kind, err := geminiText(part["type"], fp+".type")
		if err != nil || kind != "input_text" && kind != "output_text" {
			return nil, geminiConversionError(fp+".type", "只支持普通文本，不能删除 opaque 内容")
		}
		text, err = geminiText(part["text"], fp+".text")
		if err != nil {
			return nil, err
		}
		out = append(out, text)
	}
	return out, nil
}

// Stop sequences exist in Chat/Messages, but Responses has no corresponding
// native field. The private normalized field is consumed here, never forwarded.
func geminiToNative(raw []byte, protocol string) (map[string]json.RawMessage, []string, error) {
	body, adjustments, err := geminiToResponses(raw)
	if err != nil {
		return nil, nil, err
	}
	stops := body["cove_stop_sequences"]
	delete(body, "cove_stop_sequences")
	if protocol == "responses" {
		if len(stops) > 0 {
			return nil, nil, geminiConversionError("generationConfig.stopSequences", "Responses 无同义 stop 字段")
		}
		return body, adjustments, nil
	}
	if protocol != "chat_completions" && protocol != "messages" {
		return nil, nil, geminiConversionError("protocol", "转换目标尚未支持")
	}
	if protocol == "messages" && len(body["max_output_tokens"]) == 0 {
		return nil, nil, geminiConversionError("generationConfig.maxOutputTokens", "Messages 必须显式给出输出上限；不能偷偷补默认值")
	}
	converted, err := responsesToNative(body, protocol)
	if err != nil {
		return nil, nil, geminiConversionError("request", err.Error())
	}
	if len(stops) > 0 {
		if _, err = geminiStrings(stops, "generationConfig.stopSequences"); err != nil {
			return nil, nil, err
		}
		field := "stop"
		if protocol == "messages" {
			field = "stop_sequences"
		}
		converted[field] = stops
	}
	return converted, adjustments, nil
}
func nativeToGemini(raw []byte, protocol string) (map[string]json.RawMessage, []string, error) {
	if protocol == "responses" {
		body, err := geminiObject(raw, "request")
		if err != nil {
			return nil, nil, err
		}
		if _, present := body["cove_stop_sequences"]; present {
			return nil, nil, geminiConversionError("request.cove_stop_sequences", "只用于内部转换，不能作为公开 Responses 字段")
		}
		return responsesToGemini(body)
	}
	if protocol != "chat_completions" && protocol != "messages" {
		return nil, nil, geminiConversionError("protocol", "转换入口尚未支持")
	}
	envelope, err := geminiObject(raw, "request")
	if err != nil {
		return nil, nil, err
	}
	field := "stop"
	if protocol == "messages" {
		field = "stop_sequences"
	}
	stops := envelope[field]
	delete(envelope, field)
	body, compat, err := compatInput([]byte(encode(envelope)), protocol, Source{Kind: "api_key"})
	if err != nil {
		return nil, nil, geminiConversionError("request", err.Error())
	}
	if len(stops) > 0 {
		if protocol == "chat_completions" {
			var stop string
			if json.Unmarshal(stops, &stop) == nil {
				stops = json.RawMessage(encode([]string{stop}))
			}
		}
		if _, err = geminiStrings(stops, field); err != nil {
			return nil, nil, err
		}
		body["cove_stop_sequences"] = stops
	}
	converted, adjustments, err := responsesToGemini(body)
	if err != nil {
		return nil, nil, err
	}
	return converted, append(compat.adjustments, adjustments...), nil
}

const geminiConversionSchema = `CREATE TABLE IF NOT EXISTS gemini_call_bindings(
 call_id TEXT NOT NULL,key_id TEXT NOT NULL,source_id TEXT NOT NULL,account_id TEXT NOT NULL,
 generation INTEGER NOT NULL,account_generation INTEGER NOT NULL,model TEXT NOT NULL,
 part_hash TEXT NOT NULL,name TEXT NOT NULL,created_at TEXT NOT NULL,
 PRIMARY KEY(call_id,key_id,source_id,account_id,generation,account_generation,model));`

func initializeGeminiConversions(db *sql.DB) error {
	_, err := db.Exec(geminiConversionSchema)
	return err
}

type GeminiCallBinding struct {
	Store  *Store
	Key    ClientKey
	Source Source
	Model  string
}

func generatedGeminiCallID(requestID string, index int, part json.RawMessage) (string, error) {
	canonical, err := canonicalCacheJSON(part)
	if err != nil {
		return "", err
	}
	return "call_cove_" + digest(requestID + fmt.Sprintf("/%d/", index) + string(canonical))[:32], nil
}
func (b *GeminiCallBinding) Bind(callID, name string, part json.RawMessage) error {
	if b == nil {
		return nil
	}
	var outer map[string]json.RawMessage
	var function map[string]json.RawMessage
	if json.Unmarshal(part, &outer) != nil || json.Unmarshal(outer["functionCall"], &function) != nil {
		return geminiHistoryError("functionCall", "绑定内容无效")
	}
	args := function["args"]
	if len(args) == 0 {
		args = json.RawMessage("{}")
	}
	normalized := json.RawMessage(encode(map[string]any{"functionCall": map[string]any{"name": name, "args": args}}))
	canonical, err := canonicalCacheJSON(normalized)
	if err != nil {
		return err
	}
	hash := digest(string(canonical))
	_, err = b.Store.DB.Exec(`INSERT OR IGNORE INTO gemini_call_bindings(call_id,key_id,source_id,account_id,generation,account_generation,model,part_hash,name,created_at) VALUES(?,?,?,?,?,?,?,?,?,?)`, callID, b.Key.ID, b.Source.ID, b.Source.AccountID, b.Source.Generation, b.Source.AccountGeneration, b.Model, hash, name, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return storageError()
	}
	var count int
	err = b.Store.DB.QueryRow(`SELECT count(*) FROM gemini_call_bindings WHERE call_id=? AND key_id=? AND source_id=? AND account_id=? AND generation=? AND account_generation=? AND model=? AND part_hash=? AND name=?`, callID, b.Key.ID, b.Source.ID, b.Source.AccountID, b.Source.Generation, b.Source.AccountGeneration, b.Model, hash, name).Scan(&count)
	if err != nil {
		return storageError()
	}
	if count != 1 {
		return &GeminiConversionError{409, "call_id", "同一调用 ID 已绑定不同工具内容"}
	}
	return nil
}
func (b *GeminiCallBinding) Verify(body map[string]json.RawMessage) error {
	if b == nil {
		return nil
	}
	var items []map[string]json.RawMessage
	_ = json.Unmarshal(body["input"], &items)
	for i, item := range items {
		var callID, kind string
		_ = json.Unmarshal(item["call_id"], &callID)
		_ = json.Unmarshal(item["type"], &kind)
		if !strings.HasPrefix(callID, "call_cove_") || strings.HasPrefix(callID, "call_cove_history_") {
			continue
		}
		var name, hash string
		err := b.Store.DB.QueryRow(`SELECT name,part_hash FROM gemini_call_bindings WHERE call_id=? AND key_id=? AND source_id=? AND account_id=? AND generation=? AND account_generation=? AND model=?`, callID, b.Key.ID, b.Source.ID, b.Source.AccountID, b.Source.Generation, b.Source.AccountGeneration, b.Model).Scan(&name, &hash)
		if err != nil {
			if err != sql.ErrNoRows {
				return storageError()
			}
			return &GeminiConversionError{409, fmt.Sprintf("input[%d].call_id", i), "工具调用不属于当前 Key、来源、账号代次与模型"}
		}
		if kind == "function_call" {
			var actualName, args string
			_ = json.Unmarshal(item["name"], &actualName)
			_ = json.Unmarshal(item["arguments"], &args)
			part := json.RawMessage(encode(map[string]any{"functionCall": map[string]any{"name": actualName, "args": json.RawMessage(args)}}))
			canonical, e := canonicalCacheJSON(part)
			if e != nil || actualName != name || digest(string(canonical)) != hash {
				return &GeminiConversionError{409, fmt.Sprintf("input[%d]", i), "历史工具参数已改变；请重启会话"}
			}
		}
	}
	return nil
}

func geminiUsage(raw json.RawMessage) Usage {
	var value struct {
		Input     *int64 `json:"promptTokenCount"`
		Output    *int64 `json:"candidatesTokenCount"`
		Cached    *int64 `json:"cachedContentTokenCount"`
		Reasoning *int64 `json:"thoughtsTokenCount"`
		Total     *int64 `json:"totalTokenCount"`
	}
	_ = json.Unmarshal(raw, &value)
	for _, field := range []**int64{&value.Input, &value.Output, &value.Cached, &value.Reasoning, &value.Total} {
		if *field != nil && **field < 0 {
			*field = nil
		}
	}
	output := value.Output
	if output != nil && value.Reasoning != nil {
		if *output > math.MaxInt64-*value.Reasoning {
			output = nil
		} else {
			total := *output + *value.Reasoning
			output = &total
		}
	} else if value.Total != nil && value.Input != nil && *value.Total >= *value.Input {
		total := *value.Total - *value.Input
		output = &total
	}
	return Usage{Input: value.Input, Output: output, Cached: value.Cached, Reasoning: value.Reasoning}
}
func responsesUsage(value Usage) map[string]any {
	return map[string]any{"input_tokens": value.Input, "output_tokens": value.Output, "input_tokens_details": map[string]any{"cached_tokens": value.Cached}, "output_tokens_details": map[string]any{"reasoning_tokens": value.Reasoning}}
}
func geminiWireUsage(value Usage) map[string]any {
	var candidates, total *int64
	if value.Output != nil && value.Reasoning != nil && *value.Output >= *value.Reasoning {
		n := *value.Output - *value.Reasoning
		candidates = &n
	}
	if value.Input != nil && value.Output != nil && *value.Input <= math.MaxInt64-*value.Output {
		n := *value.Input + *value.Output
		total = &n
	}
	return map[string]any{"promptTokenCount": value.Input, "candidatesTokenCount": candidates, "cachedContentTokenCount": value.Cached, "thoughtsTokenCount": value.Reasoning, "totalTokenCount": total}
}
func geminiFinish(reason string) (string, string, error) {
	switch reason {
	case "STOP":
		return "completed", "", nil
	case "MAX_TOKENS":
		return "incomplete", "max_output_tokens", nil
	case "SAFETY":
		return "incomplete", "content_filter", nil
	}
	return "", "", geminiConversionError("finishReason", "目标协议无法同义表示 "+reason)
}
func geminiOutputParts(parts []json.RawMessage, requestID string, binding *GeminiCallBinding) ([]map[string]any, error) {
	items := []map[string]any{}
	usedCalls := map[string]bool{}
	texts := []any{}
	flush := func() {
		if len(texts) > 0 {
			items = append(items, map[string]any{"type": "message", "role": "assistant", "content": texts})
			texts = []any{}
		}
	}
	for i, raw := range parts {
		part, err := geminiObject(raw, fmt.Sprintf("candidates[0].content.parts[%d]", i))
		if err != nil {
			return nil, err
		}
		if err = geminiFields(part, fmt.Sprintf("candidates[0].content.parts[%d]", i), "text", "functionCall"); err != nil {
			return nil, err
		}
		if len(part) != 1 {
			return nil, geminiConversionError("content.parts", "需要明确唯一的文本或函数 Part")
		}
		if value, ok := part["text"]; ok {
			text, e := geminiText(value, "content.parts.text")
			if e != nil {
				return nil, e
			}
			texts = append(texts, map[string]any{"type": "output_text", "text": text})
			continue
		}
		flush()
		function, err := geminiObject(part["functionCall"], "functionCall")
		if err != nil {
			return nil, err
		}
		if err = geminiFields(function, "functionCall", "name", "args", "id"); err != nil {
			return nil, err
		}
		name, err := geminiText(function["name"], "functionCall.name")
		if err != nil || name == "" {
			return nil, geminiHistoryError("functionCall.name", "缺少函数名")
		}
		args := function["args"]
		if len(args) == 0 {
			args = json.RawMessage("{}")
		}
		if !jsonObject(args) {
			return nil, geminiHistoryError("functionCall.args", "需要完整对象参数")
		}
		callID := ""
		if value, ok := function["id"]; ok {
			callID, err = geminiText(value, "functionCall.id")
			if err != nil {
				return nil, err
			}
		}
		if callID == "" {
			callID, err = generatedGeminiCallID(requestID, i, raw)
			if err != nil {
				return nil, err
			}
			if err = binding.Bind(callID, name, raw); err != nil {
				return nil, err
			}
		}
		if usedCalls[callID] {
			return nil, geminiHistoryError("functionCall.id", "同一响应含重复调用 ID")
		}
		usedCalls[callID] = true
		items = append(items, map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": string(args)})
	}
	flush()
	return items, nil
}
func geminiJSONToResponses(raw []byte, requestID string, binding *GeminiCallBinding) ([]byte, error) {
	var value struct {
		ID         string `json:"responseId"`
		Model      string `json:"modelVersion"`
		Candidates []struct {
			Index   int    `json:"index"`
			Finish  string `json:"finishReason"`
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Prompt struct {
			Block string `json:"blockReason"`
		} `json:"promptFeedback"`
		Usage json.RawMessage `json:"usageMetadata"`
	}
	if json.Unmarshal(raw, &value) != nil {
		return nil, geminiHistoryError("response", "JSON 无效")
	}
	if len(value.Candidates) > 1 {
		return nil, geminiConversionError("candidates", "三协议交集不能合并多个候选")
	}
	status, reason := "", ""
	parts := []json.RawMessage{}
	if value.Prompt.Block != "" {
		if value.Prompt.Block != "SAFETY" {
			return nil, geminiConversionError("promptFeedback.blockReason", "此原生阻止原因没有同义结束状态")
		}
		status, reason = "incomplete", "content_filter"
	} else {
		if len(value.Candidates) != 1 {
			return nil, geminiHistoryError("candidates", "缺少候选终态")
		}
		if value.Candidates[0].Index != 0 {
			return nil, geminiConversionError("candidates.index", "单候选交集仅支持 index=0")
		}
		var err error
		status, reason, err = geminiFinish(value.Candidates[0].Finish)
		if err != nil {
			return nil, err
		}
		parts = value.Candidates[0].Content.Parts
	}
	items, err := geminiOutputParts(parts, requestID, binding)
	if err != nil {
		return nil, err
	}
	responseID := value.ID
	if responseID == "" {
		responseID = "resp_cove_" + digest(requestID)[:24]
	}
	out := map[string]any{"id": responseID, "object": "response", "model": value.Model, "status": status, "output": items, "usage": responsesUsage(geminiUsage(value.Usage))}
	if reason != "" {
		out["incomplete_details"] = map[string]string{"reason": reason}
	}
	return []byte(encode(out)), nil
}
func responsesJSONToGemini(raw []byte) ([]byte, error) {
	value, err := geminiObject(raw, "response")
	if err != nil {
		return nil, err
	}
	var response responseOutput
	if json.Unmarshal(raw, &response) != nil {
		return nil, geminiHistoryError("response", "JSON 无效")
	}
	finish := "STOP"
	if response.Status == "incomplete" {
		switch response.Incomplete.Reason {
		case "max_output_tokens":
			finish = "MAX_TOKENS"
		case "content_filter":
			finish = "SAFETY"
		default:
			return nil, geminiConversionError("incomplete_details.reason", "没有同义的 Gemini 结束状态")
		}
	} else if response.Status != "completed" {
		return nil, geminiConversionError("status", "不是生成终态")
	}
	var output []map[string]json.RawMessage
	_ = json.Unmarshal(value["output"], &output)
	parts := []any{}
	for i, item := range output {
		path := fmt.Sprintf("output[%d]", i)
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		switch kind {
		case "message":
			var content []map[string]json.RawMessage
			if json.Unmarshal(item["content"], &content) != nil {
				return nil, geminiHistoryError(path+".content", "内容无效")
			}
			for j, part := range content {
				fp := fmt.Sprintf("%s.content[%d]", path, j)
				if err = geminiFields(part, fp, "type", "text", "annotations", "logprobs"); err != nil {
					return nil, err
				}
				var typ string
				_ = json.Unmarshal(part["type"], &typ)
				if typ != "output_text" {
					return nil, geminiConversionError(fp+".type", "opaque/refusal 没有同义 Part")
				}
				for _, field := range []string{"annotations", "logprobs"} {
					if raw, ok := part[field]; ok {
						var entries []any
						if json.Unmarshal(raw, &entries) != nil || len(entries) > 0 {
							return nil, geminiConversionError(fp+"."+field, "目标 Gemini Part 无同义载体")
						}
					}
				}
				text, e := geminiText(part["text"], fp+".text")
				if e != nil {
					return nil, e
				}
				parts = append(parts, map[string]string{"text": text})
			}
		case "function_call":
			name, e := geminiText(item["name"], path+".name")
			if e != nil {
				return nil, e
			}
			callID, e := geminiText(item["call_id"], path+".call_id")
			if e != nil || callID == "" {
				return nil, geminiHistoryError(path+".call_id", "缺少调用 ID")
			}
			args, e := geminiText(item["arguments"], path+".arguments")
			if e != nil || !jsonObject([]byte(args)) {
				return nil, geminiHistoryError(path+".arguments", "缺少完整对象参数")
			}
			parts = append(parts, map[string]any{"functionCall": map[string]any{"id": callID, "name": name, "args": json.RawMessage(args)}})
		default:
			return nil, geminiConversionError(path+".type", "不能删除或转换 opaque/server tool 输出")
		}
	}
	var usage Usage
	mergeUsage(&usage, value["usage"])
	return []byte(encode(map[string]any{"responseId": response.ID, "modelVersion": response.Model, "candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": parts}, "finishReason": finish}}, "usageMetadata": geminiWireUsage(usage)})), nil
}

// GeminiNativeStream emits the existing Responses event intersection. Native
// SSE parts are deltas, so every text chunk is appended, never replaced by the
// latest Content. Terminal publication waits for EOF to include trailing usage.
type GeminiNativeStream struct {
	Native      *nativeOutput
	requestID   string
	binding     *GeminiCallBinding
	next        int
	finish      string
	ended       bool
	callHashes  map[string]string
	usage       map[string]json.RawMessage
	textBlock   int
	lastWasText bool
}

func newGeminiNativeStream(max int64, requestID string, binding *GeminiCallBinding) *GeminiNativeStream {
	return &GeminiNativeStream{Native: newNativeOutput("gemini", max, true), requestID: requestID, binding: binding, callHashes: map[string]string{}, usage: map[string]json.RawMessage{}}
}
func (g *GeminiNativeStream) Frame(frame []byte) error {
	if g.ended {
		return geminiHistoryError("stream", "终态之后出现事件")
	}
	data, _ := sseData(frame)
	if len(data) == 0 {
		return nil
	}
	var value struct {
		ID         string `json:"responseId"`
		Model      string `json:"modelVersion"`
		Candidates []struct {
			Index   int    `json:"index"`
			Finish  string `json:"finishReason"`
			Content struct {
				Parts []json.RawMessage `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
		Prompt struct {
			Block string `json:"blockReason"`
		} `json:"promptFeedback"`
		Usage map[string]json.RawMessage `json:"usageMetadata"`
		Error json.RawMessage            `json:"error"`
	}
	if json.Unmarshal(data, &value) != nil {
		return geminiHistoryError("stream", "事件 JSON 无效")
	}
	if len(value.Error) > 0 {
		return geminiConversionError("error", "原生 Gemini 错误不能作为成功终态")
	}
	if len(value.Candidates) > 1 {
		return geminiConversionError("candidates", "转换不能合并多个候选")
	}
	for key, raw := range value.Usage {
		g.usage[key] = raw
	}
	g.Native.usage = geminiUsage(json.RawMessage(encode(g.usage)))
	responseID := value.ID
	if responseID == "" && !g.Native.started {
		responseID = "resp_cove_" + digest(g.requestID)[:24]
	}
	if err := g.Native.start(responseID, value.Model); err != nil {
		return err
	}
	if value.Prompt.Block != "" {
		if value.Prompt.Block != "SAFETY" {
			return geminiConversionError("promptFeedback.blockReason", "无同义结束状态")
		}
		g.finish = "content_filter"
	}
	for _, candidate := range value.Candidates {
		if candidate.Index != 0 {
			return geminiConversionError("candidates.index", "单候选交集仅支持 index=0")
		}
		if g.finish != "" && len(candidate.Content.Parts) > 0 {
			return geminiHistoryError("content.parts", "候选结束后又出现内容")
		}
		for _, raw := range candidate.Content.Parts {
			items, err := geminiOutputParts([]json.RawMessage{raw}, g.requestID+fmt.Sprintf("/%d", g.next), g.binding)
			if err != nil {
				return err
			}
			g.next++
			for _, item := range items {
				if item["type"] == "message" {
					if !g.lastWasText {
						g.textBlock = -(g.next + 1)
					}
					g.lastWasText = true
					oi, err := g.Native.add(g.textBlock, "message", "", "", "")
					if err != nil {
						return err
					}
					for _, part := range item["content"].([]any) {
						if err = g.Native.delta(oi, part.(map[string]any)["text"].(string)); err != nil {
							return err
						}
					}
				} else {
					g.lastWasText = false
					callID := item["call_id"].(string)
					arguments := item["arguments"].(string)
					hash := digest(item["name"].(string) + "\n" + arguments)
					if old, exists := g.callHashes[callID]; exists {
						if old != hash {
							return geminiHistoryError("functionCall.id", "同 ID 的参数发生冲突")
						}
						continue
					}
					g.callHashes[callID] = hash
					oi, err := g.Native.add(g.next, "function_call", callID, item["name"].(string), "")
					if err != nil {
						return err
					}
					if err = g.Native.delta(oi, arguments); err != nil {
						return err
					}
				}
			}
		}
		if candidate.Finish != "" {
			status, reason, err := geminiFinish(candidate.Finish)
			if err != nil {
				return err
			}
			if status == "completed" {
				g.finish = "stop"
			} else if reason == "max_output_tokens" {
				g.finish = "length"
			} else {
				g.finish = "content_filter"
			}
		}
	}
	return nil
}
func (g *GeminiNativeStream) End() error {
	if g.ended {
		return geminiHistoryError("stream", "重复终态")
	}
	if g.finish == "" {
		return geminiHistoryError("finishReason", "流提前结束；不生成伪成功终态")
	}
	g.ended = true
	g.Native.finish = g.finish
	emit := g.Native.emit
	g.Native.emit = nil
	err := g.Native.complete()
	g.Native.emit = emit
	if err != nil {
		return err
	}
	if int64(len(encode(g.Native.response))) > g.Native.max {
		g.Native.terminal = false
		return geminiHistoryError("response", "转换聚合结果超出本机限制")
	}
	status, _ := g.Native.response["status"].(string)
	return g.Native.event("response."+status, map[string]any{"response": g.Native.response})
}

// ResponsesToGeminiStream reuses final-output reconciliation rather than
// emitting a second copy of the complete answer after sending text deltas.
type ResponsesToGeminiStream struct {
	ID, Model    string
	max, written int64
	terminal     bool
	blocked      bool
	observedSize int64
	observed     map[string]string
	texts        map[string]string
	calls        map[int]map[string]json.RawMessage
	args         map[int]string
}

func newResponsesToGeminiStream(max int64) *ResponsesToGeminiStream {
	return &ResponsesToGeminiStream{max: max, observed: map[string]string{}, texts: map[string]string{}, calls: map[int]map[string]json.RawMessage{}, args: map[int]string{}}
}
func (g *ResponsesToGeminiStream) wire(parts []any, finish string, usage map[string]any) ([]byte, error) {
	value := map[string]any{"responseId": g.ID, "modelVersion": g.Model, "candidates": []any{map[string]any{"index": 0, "content": map[string]any{"role": "model", "parts": parts}}}}
	candidate := value["candidates"].([]any)[0].(map[string]any)
	if finish != "" {
		candidate["finishReason"] = finish
	}
	if usage != nil {
		value["usageMetadata"] = usage
	}
	wire := []byte("data: " + encode(value) + "\n\n")
	g.written += int64(len(wire))
	if g.written > g.max {
		return nil, geminiHistoryError("stream", "转换输出超出本机限制")
	}
	return wire, nil
}
func (g *ResponsesToGeminiStream) Event(frame []byte) ([]byte, error) {
	data, event := sseData(frame)
	if len(data) == 0 {
		return nil, nil
	}
	if g.terminal {
		return nil, geminiHistoryError("stream", "重复或终态之后的事件")
	}
	var value struct {
		Type         string                     `json:"type"`
		Response     json.RawMessage            `json:"response"`
		OutputIndex  int                        `json:"output_index"`
		ContentIndex int                        `json:"content_index"`
		Delta        string                     `json:"delta"`
		Item         map[string]json.RawMessage `json:"item"`
	}
	if json.Unmarshal(data, &value) != nil {
		return nil, geminiHistoryError("stream", "Responses 事件 JSON 无效")
	}
	if event == "" {
		event = value.Type
	}
	if value.Type != "" && event != value.Type {
		return nil, geminiHistoryError("stream.type", "event 与 payload type 冲突")
	}
	if value.OutputIndex < 0 || value.ContentIndex < 0 {
		return nil, geminiHistoryError("stream.index", "索引无效")
	}
	switch event {
	case "response.created":
		var response struct{ ID, Model string }
		if json.Unmarshal(value.Response, &response) != nil {
			return nil, geminiHistoryError("response.created", "缺少响应")
		}
		g.ID, g.Model = response.ID, response.Model
		return nil, nil
	case "response.output_item.added":
		var kind string
		_ = json.Unmarshal(value.Item["type"], &kind)
		if kind == "function_call" {
			if g.calls[value.OutputIndex] != nil {
				return nil, geminiHistoryError("output_index", "重复工具块")
			}
			g.blocked = true
			g.calls[value.OutputIndex] = value.Item
			var initial string
			_ = json.Unmarshal(value.Item["arguments"], &initial)
			g.args[value.OutputIndex] = initial
		} else if kind != "message" {
			return nil, geminiConversionError("output.type", "目标 Gemini 无 opaque/server tool 载体")
		}
		return nil, nil
	case "response.output_text.delta":
		key := fmt.Sprintf("%d:%d", value.OutputIndex, value.ContentIndex)
		g.observedSize += int64(len(value.Delta))
		if g.observedSize > g.max {
			return nil, geminiHistoryError("stream", "文本聚合超出本机限制")
		}
		g.observed[key] += value.Delta
		if g.blocked {
			return nil, nil
		}
		g.texts[key] += value.Delta
		return g.wire([]any{map[string]string{"text": value.Delta}}, "", nil)
	case "response.function_call_arguments.delta":
		if g.calls[value.OutputIndex] == nil {
			return nil, geminiHistoryError("output_index", "工具参数在声明之前出现")
		}
		g.args[value.OutputIndex] += value.Delta
		if int64(len(g.args[value.OutputIndex])) > g.max {
			return nil, geminiHistoryError("arguments", "工具参数超限")
		}
		return nil, nil
	case "response.output_item.done", "response.content_part.added", "response.content_part.done", "response.output_text.done", "response.function_call_arguments.done", "response.in_progress":
		return nil, nil
	case "response.completed", "response.incomplete":
		converted, err := responsesJSONToGemini(value.Response)
		if err != nil {
			return nil, err
		}
		var final struct {
			ID         string `json:"responseId"`
			Model      string `json:"modelVersion"`
			Candidates []struct {
				Finish string `json:"finishReason"`
			} `json:"candidates"`
			Usage map[string]any `json:"usageMetadata"`
		}
		_ = json.Unmarshal(converted, &final)
		g.ID, g.Model = final.ID, final.Model
		var response responseOutput
		if json.Unmarshal(value.Response, &response) != nil {
			return nil, geminiHistoryError("response", "终态无效")
		}
		parts := []any{}
		seen := map[string]bool{}
		for oi, item := range response.Output {
			if item.Type == "message" {
				for ci, content := range item.Content {
					key := fmt.Sprintf("%d:%d", oi, ci)
					seen[key] = true
					prefix := g.texts[key]
					if !strings.HasPrefix(content.Text, g.observed[key]) {
						return nil, geminiHistoryError("output.content", "终态与已交付文本不一致")
					}
					if len(content.Text) > len(prefix) {
						parts = append(parts, map[string]string{"text": content.Text[len(prefix):]})
					}
				}
			} else if item.Type == "function_call" {
				arguments := g.args[oi]
				if !strings.HasPrefix(item.Arguments, arguments) {
					return nil, geminiHistoryError("output.arguments", "终态与参数 delta 不一致")
				}
				if prior := g.calls[oi]; prior != nil {
					var name, callID string
					_ = json.Unmarshal(prior["name"], &name)
					_ = json.Unmarshal(prior["call_id"], &callID)
					if name != item.Name || callID != item.CallID {
						return nil, geminiHistoryError("output.call_id", "工具身份发生改变")
					}
				}
				parts = append(parts, map[string]any{"functionCall": map[string]any{"id": item.CallID, "name": item.Name, "args": json.RawMessage(item.Arguments)}})
			}
		}
		for key, text := range g.observed {
			if text != "" && !seen[key] {
				return nil, geminiHistoryError("output.content", "终态丢失已交付文本")
			}
		}
		wire, err := g.wire(parts, final.Candidates[0].Finish, final.Usage)
		if err == nil {
			g.terminal = true
		}
		return wire, err
	case "error", "response.failed":
		return nil, geminiConversionError("error", "上游错误不是成功 Gemini 终态")
	default:
		return nil, geminiConversionError("stream.event", event+" 尚无同义事件映射")
	}
}
func (g *ResponsesToGeminiStream) End() error {
	if !g.terminal {
		return geminiHistoryError("stream", "缺少正式终态；不能补一个成功事件")
	}
	return nil
}
func geminiConversionStreamError(requestID string) []byte {
	return []byte("data: " + encode(map[string]any{"error": map[string]any{"code": 502, "status": "UNAVAILABLE", "message": "转换流未完整结束；未重试", "request_id": requestID}}) + "\n\n")
}
