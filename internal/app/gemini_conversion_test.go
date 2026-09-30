package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const conversionGeminiRequest = `{"model":"fixture-model","stream":false,"systemInstruction":{"parts":[{"text":"system"}]},"contents":[{"role":"user","parts":[{"text":"question"}]},{"role":"model","parts":[{"functionCall":{"id":"one","name":"lookup","args":{"q":"a"}}},{"functionCall":{"id":"two","name":"lookup","args":{"q":"b"}}}]},{"role":"user","parts":[{"functionResponse":{"id":"two","name":"lookup","response":{"answer":"b"}}},{"functionResponse":{"id":"one","name":"lookup","response":{"answer":"a"}}}]}],"generationConfig":{"maxOutputTokens":32,"temperature":0.2,"topP":0.9},"tools":[{"functionDeclarations":[{"name":"lookup","description":"synthetic","parameters":{"type":"OBJECT","properties":{"q":{"type":"STRING"}},"required":["q"]}}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["lookup"]}}}`

func conversionObject(t *testing.T, raw []byte) map[string]json.RawMessage {
	t.Helper()
	value, err := geminiObject(raw, "test")
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func conversionError(t *testing.T, err error, status int, field string) {
	t.Helper()
	var value *GeminiConversionError
	if !errors.As(err, &value) || value.Status != status || !strings.Contains(value.Field, field) {
		t.Fatalf("error=%v want status=%d field=%s", err, status, field)
	}
}

func TestSpecGeminiConversionSixRequestDirections(t *testing.T) {
	responses, _, err := geminiToNative([]byte(conversionGeminiRequest), "responses")
	if err != nil {
		t.Fatal(err)
	}
	var history []map[string]json.RawMessage
	if json.Unmarshal(responses["input"], &history) != nil || len(history) != 6 {
		t.Fatalf("history=%s", responses["input"])
	}
	if string(history[2]["call_id"]) != `"one"` || string(history[3]["call_id"]) != `"two"` || string(history[4]["call_id"]) != `"two"` || string(history[5]["call_id"]) != `"one"` {
		t.Fatal("same-name parallel calls/results crossed")
	}
	if string(responses["max_output_tokens"]) != "32" || string(responses["temperature"]) != "0.2" || string(responses["top_p"]) != "0.9" {
		t.Fatal("generation config mapping")
	}
	for _, protocol := range []string{"responses", "chat_completions", "messages"} {
		wire, _, err := geminiToNative([]byte(conversionGeminiRequest), protocol)
		if err != nil {
			t.Fatal(protocol, err)
		}
		back, adjustments, err := nativeToGemini([]byte(encode(wire)), protocol)
		if err != nil {
			t.Fatal(protocol, err)
		}
		if len(adjustments) > 0 {
			t.Fatal("unexpected adjustment", adjustments)
		}
		var contents []struct {
			Role  string                       `json:"role"`
			Parts []map[string]json.RawMessage `json:"parts"`
		}
		if json.Unmarshal(back["contents"], &contents) != nil || len(contents) != 3 {
			t.Fatalf("%s contents=%s", protocol, back["contents"])
		}
		if contents[0].Role != "user" || contents[1].Role != "model" || contents[2].Role != "user" {
			t.Fatal("roles changed")
		}
		for i, want := range []string{"two", "one"} {
			var result struct {
				Name, ID string
				Response map[string]string
			}
			if json.Unmarshal(contents[2].Parts[i]["functionResponse"], &result) != nil || result.ID != want || result.Name != "lookup" {
				t.Fatal("IDs lost on reverse conversion", protocol, result)
			}
		}
		if !bytes.Contains(back["tools"], []byte(`"parametersJsonSchema"`)) || !bytes.Contains(back["tools"], []byte(`"type":"string"`)) {
			t.Fatal("function schema corrupted")
		}
	}
}
func TestSpecGeminiConversionStopsAndSystemMerge(t *testing.T) {
	withStops := strings.Replace(conversionGeminiRequest, `"maxOutputTokens":32`, `"maxOutputTokens":32,"stopSequences":["END","终"]`, 1)
	_, _, err := geminiToNative([]byte(withStops), "responses")
	conversionError(t, err, 422, "stopSequences")
	for _, protocol := range []string{"chat_completions", "messages"} {
		wire, _, err := geminiToNative([]byte(withStops), protocol)
		if err != nil {
			t.Fatal(err)
		}
		field := "stop"
		if protocol == "messages" {
			field = "stop_sequences"
		}
		if string(wire[field]) != `["END","终"]` {
			t.Fatal("stops changed")
		}
		back, _, err := nativeToGemini([]byte(encode(wire)), protocol)
		if err != nil || !bytes.Contains(back["generationConfig"], []byte(`"stopSequences":["END","终"]`)) {
			t.Fatal("stop reverse", err)
		}
	}
	request := `{"model":"fixture-model","instructions":"first","input":[{"role":"system","content":"second"},{"role":"developer","content":"third"},{"role":"user","content":"question"}]}`
	out, adjustments, err := nativeToGemini([]byte(request), "responses")
	if err != nil {
		t.Fatal(err)
	}
	if len(adjustments) != 1 || adjustments[0] != "system_roles_merged" || !bytes.Contains(out["systemInstruction"], []byte(`first\n\nsecond\n\nthird`)) {
		t.Fatal("system merge not disclosed", adjustments, string(out["systemInstruction"]))
	}
}
func TestSpecGeminiConversionUnsupportedAndMalformedHistories(t *testing.T) {
	for _, field := range []string{`"safetySettings":[]`, `"generationConfig":{"thinkingConfig":{"thinkingBudget":1}}`, `"generationConfig":{"responseMimeType":"application/json"}`, `"generationConfig":{"candidateCount":2}`} {
		body := `{"model":"fixture-model","contents":[{"parts":[{"text":"hello"}]}],` + field + `}`
		_, _, err := geminiToResponses([]byte(body))
		var conversion *GeminiConversionError
		if !errors.As(err, &conversion) || conversion.Status != 422 {
			t.Fatal("unsupported field silently removed", body, err)
		}
	}
	for _, part := range []string{`{"text":"thought","thoughtSignature":"NEVER_DROP_SIGNATURE"}`, `{"inlineData":{"mimeType":"image/png","data":"AA=="}}`, `{"fileData":{"fileUri":"foreign"}}`} {
		body := `{"model":"fixture-model","contents":[{"role":"model","parts":[` + part + `]}]}`
		_, _, err := geminiToResponses([]byte(body))
		conversionError(t, err, 422, "parts")
	}
	for _, field := range []string{`"reasoning":{"effort":"high"}`, `"previous_response_id":"old"`, `"conversation":"state"`, `"parallel_tool_calls":false`, `"cove_stop_sequences":["private"]`, `"text":{"format":{"type":"json_schema"}}`} {
		body := `{"model":"fixture-model","input":"text",` + field + `}`
		_, _, err := nativeToGemini([]byte(body), "responses")
		var conversion *GeminiConversionError
		if !errors.As(err, &conversion) || conversion.Status != 422 {
			t.Fatal("Responses field dropped", body, err)
		}
	}
	noIDs := strings.ReplaceAll(strings.ReplaceAll(conversionGeminiRequest, `"id":"one",`, ""), `"id":"two",`, "")
	_, _, err := geminiToResponses([]byte(noIDs))
	conversionError(t, err, 400, "functionResponse.id")
	body := `{"model":"fixture-model","contents":[{"role":"model","parts":[{"functionCall":{"name":"lookup","args":{"q":"a"}}},{"functionCall":{"name":"lookup","args":{"q":"b"}}}]}]}`
	a, _, err := geminiToResponses([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := geminiToResponses([]byte(body))
	if err != nil || !bytes.Equal(a["input"], b["input"]) {
		t.Fatal("current request IDs unstable")
	}
	var items []struct {
		ID string `json:"call_id"`
	}
	_ = json.Unmarshal(a["input"], &items)
	if len(items) != 2 || items[0].ID == items[1].ID {
		t.Fatal("parallel names got same generated ID")
	}
	_, _, err = geminiToNative([]byte(`{"model":"fixture-model","contents":[{"parts":[{"text":"x"}]}]}`), "messages")
	conversionError(t, err, 422, "maxOutputTokens")
}
func TestSpecGeminiConversionOutputAndUsage(t *testing.T) {
	gemini := `{"responseId":"native","modelVersion":"fixture-model","candidates":[{"index":0,"content":{"parts":[{"text":"answer"},{"functionCall":{"id":"one","name":"lookup","args":{"q":"a"}}},{"functionCall":{"id":"two","name":"lookup","args":{"q":"b"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":5,"thoughtsTokenCount":3,"cachedContentTokenCount":2,"totalTokenCount":19}}`
	responses, err := geminiJSONToResponses([]byte(gemini), "request", nil)
	if err != nil {
		t.Fatal(err)
	}
	var normalized responseOutput
	_ = json.Unmarshal(responses, &normalized)
	var usage Usage
	mergeUsage(&usage, normalized.Usage)
	if usage.Output == nil || *usage.Output != 8 || usage.Reasoning == nil || *usage.Reasoning != 3 || usage.Input == nil || *usage.Input != 11 {
		t.Fatalf("thought tokens double counted/lost: %+v", usage)
	}
	for _, protocol := range []string{"chat_completions", "messages"} {
		out := &compatOutput{protocol: protocol, max: 10000, blocks: map[string]*compatBlock{}}
		wire, err := out.finish(responses)
		if err != nil {
			t.Fatal(protocol, err)
		}
		if !bytes.Contains(wire, []byte("lookup")) || !bytes.Contains(wire, []byte("answer")) {
			t.Fatal("content/tools lost", protocol)
		}
		if protocol == "chat_completions" && !bytes.Contains(wire, []byte(`"completion_tokens":8`)) {
			t.Fatal("Chat total output usage")
		}
	}
	back, err := responsesJSONToGemini(responses)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(back, []byte(`"candidatesTokenCount":5`)) || !bytes.Contains(back, []byte(`"thoughtsTokenCount":3`)) || !bytes.Contains(back, []byte(`"totalTokenCount":19`)) {
		t.Fatal("Gemini usage repeated reasoning")
	}
	partial := geminiUsage(json.RawMessage(`{"promptTokenCount":11}`))
	if partial.Output != nil || partial.Reasoning != nil {
		t.Fatal("missing provider usage invented")
	}
	_, err = geminiJSONToResponses([]byte(`{"candidates":[{"finishReason":"STOP"},{"finishReason":"STOP"}]}`), "request", nil)
	conversionError(t, err, 422, "candidates")
	_, err = geminiJSONToResponses([]byte(`{"candidates":[{"index":2,"finishReason":"STOP"}]}`), "request", nil)
	conversionError(t, err, 422, "candidates.index")
	safety, err := geminiJSONToResponses([]byte(`{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":5}}`), "request", nil)
	if err != nil || !bytes.Contains(safety, []byte(`"reason":"content_filter"`)) {
		t.Fatal("native safety block became success text", err)
	}
	_, err = responsesJSONToGemini([]byte(`{"status":"completed","output":[{"type":"reasoning","encrypted_content":"OPAQUE"}]}`))
	conversionError(t, err, 422, "output[0].type")
}
func TestSpecGeminiConversionGeneratedCallBinding(t *testing.T) {
	a := extendedApp(t, nil)
	if err := initializeGeminiConversions(a.Store.DB); err != nil {
		t.Fatal(err)
	}
	key, _ := a.Store.keyByDigest(digest("test-client-key"))
	src, _ := a.Store.source("source")
	binding := &GeminiCallBinding{Store: a.Store, Key: key, Source: src, Model: "fixture-model"}
	output := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup","args":{"q":"PRIVATE_ARGUMENT_SENTINEL"}}},{"functionCall":{"name":"lookup","args":{"q":"other"}}}]},"finishReason":"STOP"}]}`
	response, err := geminiJSONToResponses([]byte(output), "current-request", binding)
	if err != nil {
		t.Fatal(err)
	}
	var result responseOutput
	_ = json.Unmarshal(response, &result)
	if result.Output[0].CallID == result.Output[1].CallID || !strings.HasPrefix(result.Output[0].CallID, "call_cove_") {
		t.Fatal("generated IDs merged parallel names")
	}
	request := map[string]json.RawMessage{"model": json.RawMessage(`"fixture-model"`), "input": json.RawMessage(encode([]any{map[string]any{"type": "function_call", "call_id": result.Output[0].CallID, "name": "lookup", "arguments": result.Output[0].Arguments}, map[string]any{"type": "function_call_output", "call_id": result.Output[0].CallID, "output": "result"}}))}
	if err = binding.Verify(request); err != nil {
		t.Fatal(err)
	}
	other := *binding
	other.Key.ID = "other-key"
	conversionError(t, other.Verify(request), 409, "call_id")
	other = *binding
	other.Source.AccountGeneration++
	conversionError(t, other.Verify(request), 409, "call_id")
	other = *binding
	other.Source.Generation++
	conversionError(t, other.Verify(request), 409, "call_id")
	other = *binding
	other.Model = "other-model"
	conversionError(t, other.Verify(request), 409, "call_id")
	changed := map[string]json.RawMessage{"input": json.RawMessage(strings.Replace(string(request["input"]), "PRIVATE_ARGUMENT_SENTINEL", "edited", 1))}
	conversionError(t, binding.Verify(changed), 409, "input[0]")
	var stored string
	if err = a.Store.DB.QueryRow("SELECT group_concat(part_hash||name) FROM gemini_call_bindings").Scan(&stored); err != nil || strings.Contains(stored, "PRIVATE_ARGUMENT_SENTINEL") {
		t.Fatal("private arguments persisted", err)
	}
	_, err = geminiJSONToResponses([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"lookup"},"thoughtSignature":"OPAQUE"}]},"finishReason":"STOP"}]}`), "signed", binding)
	conversionError(t, err, 422, "parts")
}
func TestSpecGeminiConversionStreamsInterleavingAndUsage(t *testing.T) {
	native := newGeminiNativeStream(20000, "stream-request", nil)
	reverse := newResponsesToGeminiStream(20000)
	var responsesWire, geminiWire bytes.Buffer
	native.Native.emit = func(frame []byte) error {
		responsesWire.Write(frame)
		converted, err := reverse.Event(frame)
		if err != nil {
			return err
		}
		geminiWire.Write(converted)
		return nil
	}
	frames := []string{
		`{"responseId":"stream-native","modelVersion":"fixture-model","candidates":[{"index":0,"content":{"parts":[{"text":"A"},{"functionCall":{"name":"lookup","args":{"q":"a"}}},{"functionCall":{"name":"lookup","args":{"q":"b"}}},{"text":"B"}]}}]}`,
		`{"candidates":[{"index":0,"content":{"parts":[{"text":"C"}]},"finishReason":"STOP"}]}`,
		`{"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":5,"thoughtsTokenCount":3}}`,
	}
	for _, frame := range frames {
		if err := native.Frame([]byte("data: " + frame + "\n\n")); err != nil {
			t.Fatal(err)
		}
	}
	if err := native.End(); err != nil {
		t.Fatal(err)
	}
	if err := reverse.End(); err != nil {
		t.Fatal(err)
	}
	if len(native.Native.items) != 4 || native.Native.items[0]["type"] != "message" || native.Native.items[1]["type"] != "function_call" || native.Native.items[2]["type"] != "function_call" || native.Native.items[3]["type"] != "message" {
		t.Fatal("interleaved output order changed", native.Native.items)
	}
	if native.Native.items[1]["call_id"] == native.Native.items[2]["call_id"] {
		t.Fatal("parallel function IDs crossed")
	}
	var delivered []string
	observer := newSSE(20000, func(frame []byte) error {
		data, _ := sseData(frame)
		var value struct {
			Candidates []struct {
				Content struct {
					Parts []map[string]json.RawMessage `json:"parts"`
				} `json:"content"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		for _, candidate := range value.Candidates {
			for _, part := range candidate.Content.Parts {
				if text, ok := part["text"]; ok {
					delivered = append(delivered, "text:"+string(text))
				} else {
					delivered = append(delivered, "call")
				}
			}
		}
		return nil
	}, nil)
	if err := observer.Feed(geminiWire.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := observer.End(); err != nil {
		t.Fatal(err)
	}
	if strings.Join(delivered, ",") != `text:"A",call,call,text:"BC"` {
		t.Fatal("delivered text/tool order changed or duplicated", delivered, geminiWire.String())
	}
	if strings.Count(geminiWire.String(), `"functionCall"`) != 2 || !strings.Contains(geminiWire.String(), `"candidatesTokenCount":5`) || !strings.Contains(responsesWire.String(), `"output_tokens":8`) {
		t.Fatal("tool output/usage omitted", geminiWire.String())
	}
}
func TestSpecGeminiConversionStreamFailuresAreNotSuccess(t *testing.T) {
	native := newGeminiNativeStream(1000, "r", nil)
	if err := native.Frame([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}` + "\n\n")); err != nil {
		t.Fatal(err)
	}
	if native.End() == nil {
		t.Fatal("EOF created successful terminal")
	}
	native = newGeminiNativeStream(1000, "r", nil)
	conversionError(t, native.Frame([]byte(`data: {"candidates":[{"index":0},{"index":1}]}`+"\n\n")), 422, "candidates")
	native = newGeminiNativeStream(1000, "r", nil)
	conversionError(t, native.Frame([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"thought","thoughtSignature":"OPAQUE"}]}}]}`+"\n\n")), 422, "parts")
	reverse := newResponsesToGeminiStream(1000)
	_, err := reverse.Event([]byte("event: response.output_text.delta\ndata: {\"output_index\":0,\"content_index\":0,\"delta\":\"already sent\"}\n\n"))
	if err != nil {
		t.Fatal(err)
	}
	final := `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"different"}]}]}`
	_, err = reverse.Event([]byte("event: response.completed\ndata: " + encode(map[string]any{"response": json.RawMessage(final)}) + "\n\n"))
	conversionError(t, err, 400, "output.content")
	if reverse.End() == nil {
		t.Fatal("failed conversion claimed terminal")
	}
	if bytes.Contains(geminiConversionStreamError("r"), []byte("finishReason")) {
		t.Fatal("error event claims successful Gemini finish")
	}
}

func TestSpecGeminiConversionNativeOutputsBackToGemini(t *testing.T) {
	fixtures := map[string]string{
		"chat_completions": `{"id":"chat","model":"fixture-model","choices":[{"message":{"role":"assistant","content":"answer","tool_calls":[{"id":"one","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"a\"}"}},{"id":"two","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"b\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":11,"completion_tokens":8,"completion_tokens_details":{"reasoning_tokens":3}}}`,
		"messages":         `{"id":"msg","model":"fixture-model","content":[{"type":"text","text":"answer"},{"type":"tool_use","id":"one","name":"lookup","input":{"q":"a"}},{"type":"tool_use","id":"two","name":"lookup","input":{"q":"b"}}],"stop_reason":"tool_use","usage":{"input_tokens":11,"output_tokens":8}}`,
	}
	for protocol, raw := range fixtures {
		normalized, err := nativeJSON([]byte(raw), protocol)
		if err != nil {
			t.Fatal(protocol, err)
		}
		gemini, err := responsesJSONToGemini(normalized)
		if err != nil {
			t.Fatal(protocol, err)
		}
		if !bytes.Contains(gemini, []byte(`"text":"answer"`)) || !bytes.Contains(gemini, []byte(`"id":"one"`)) || !bytes.Contains(gemini, []byte(`"id":"two"`)) || !bytes.Contains(gemini, []byte(`"finishReason":"STOP"`)) {
			t.Fatal("native text/tools/finish lost", protocol, string(gemini))
		}
		if protocol == "chat_completions" && !bytes.Contains(gemini, []byte(`"candidatesTokenCount":5`)) {
			t.Fatal("reasoning counted twice", string(gemini))
		}
		if protocol == "messages" && !bytes.Contains(gemini, []byte(`"candidatesTokenCount":null`)) {
			t.Fatal("unknown reasoning guessed", string(gemini))
		}
	}
}
func TestSpecGeminiConversionBoundsNeverPublishSuccess(t *testing.T) {
	native := newGeminiNativeStream(20, "r", nil)
	if err := native.Frame([]byte(`data: {"candidates":[{"content":{"parts":[{"text":"A"}]},"finishReason":"STOP"}]}` + "\n\n")); err != nil {
		t.Fatal(err)
	}
	if native.End() == nil || native.Native.terminal {
		t.Fatal("oversize aggregate claimed successful terminal")
	}
	reverse := newResponsesToGeminiStream(1)
	final := `{"id":"r","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"A"}]}]}`
	_, err := reverse.Event([]byte("event: response.completed\ndata: " + encode(map[string]any{"response": json.RawMessage(final)}) + "\n\n"))
	if err == nil || reverse.End() == nil {
		t.Fatal("oversize native SSE claimed successful terminal")
	}
	_, err = geminiJSONToResponses([]byte(`{"candidates":[{"content":{"parts":[{"functionCall":{"id":"same","name":"lookup","args":{}}},{"functionCall":{"id":"same","name":"lookup","args":{"q":"different"}}}]},"finishReason":"STOP"}]}`), "r", nil)
	conversionError(t, err, 400, "functionCall.id")
}

func TestSpecGeminiConversionSixStreamDirections(t *testing.T) {
	for _, protocol := range []string{"responses", "chat_completions", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			fromGemini := newGeminiNativeStream(20000, "six-directions", nil)
			var nativeWire bytes.Buffer
			var output *compatOutput
			if protocol != "responses" {
				localInput := int64(11)
				output = &compatOutput{protocol: protocol, stream: true, includeUsage: true, max: 20000, blocks: map[string]*compatBlock{}, wireInput: &localInput}
			}
			fromGemini.Native.emit = func(frame []byte) error {
				if output == nil {
					nativeWire.Write(frame)
					return nil
				}
				converted, err := output.event(frame)
				nativeWire.Write(converted)
				return err
			}
			frames := []string{
				`{"responseId":"stream-source","modelVersion":"fixture-model","candidates":[{"content":{"parts":[{"text":"answer"},{"functionCall":{"id":"one","name":"lookup","args":{"q":"a"}}},{"functionCall":{"id":"two","name":"lookup","args":{"q":"b"}}}]}}]}`,
				`{"candidates":[{"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":5,"thoughtsTokenCount":3}}`,
			}
			for _, raw := range frames {
				if err := fromGemini.Frame([]byte("data: " + raw + "\n\n")); err != nil {
					t.Fatal(err)
				}
			}
			if err := fromGemini.End(); err != nil {
				t.Fatal(err)
			}
			if output != nil {
				final, err := output.finish(output.final)
				if err != nil {
					t.Fatal(err)
				}
				nativeWire.Write(final)
			}
			toGemini := newResponsesToGeminiStream(20000)
			var geminiWire bytes.Buffer
			emit := func(frame []byte) error {
				converted, err := toGemini.Event(frame)
				geminiWire.Write(converted)
				return err
			}
			var source *nativeOutput
			if protocol != "responses" {
				source = newNativeOutput(protocol, 20000, true)
				source.emit = emit
			}
			observer := newSSE(20000, func(frame []byte) error {
				if source == nil {
					return emit(frame)
				}
				return source.frame(frame)
			}, nil)
			if err := observer.Feed(nativeWire.Bytes()); err != nil {
				t.Fatal(err)
			}
			if err := observer.End(); err != nil {
				t.Fatal(err)
			}
			if source != nil && !source.terminal {
				t.Fatal("missing native terminal")
			}
			if err := toGemini.End(); err != nil {
				t.Fatal(err)
			}
			if strings.Count(geminiWire.String(), `"text":"answer"`) != 1 || strings.Count(geminiWire.String(), `"functionCall"`) != 2 || !bytes.Contains(geminiWire.Bytes(), []byte(`"id":"one"`)) || !bytes.Contains(geminiWire.Bytes(), []byte(`"id":"two"`)) {
				t.Fatal("stream text/function identity lost", geminiWire.String())
			}
		})
	}
}

func TestSpecGeminiConversionResponsesEventIdentity(t *testing.T) {
	g := newResponsesToGeminiStream(10000)
	_, err := g.Event([]byte(`data: {"type":"response.created","response":{"id":"r","model":"fixture-model"}}` + "\n\n"))
	if err != nil || g.ID != "r" {
		t.Fatal("Responses payload event identity lost", err)
	}
	_, err = g.Event([]byte("event: response.completed\ndata: {\"type\":\"response.failed\"}\n\n"))
	conversionError(t, err, 400, "stream.type")
}
