package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	urlpath "path"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

const nativeMediaLimit int64 = 32 << 20
const nativeJSONLimit int64 = 8 << 20
const nativeAdmissionLimit int64 = 64 << 10

type nativeAdmissionError struct {
	Code           int
	Message, Field string
}

func (e *nativeAdmissionError) Error() string { return e.Message }

type nativeOperationSpec struct {
	Name, Path string
	Multipart  bool
	Media      bool
}

func nativeOperation(path string) (nativeOperationSpec, bool) {
	switch path {
	case "/v1/images/generations":
		return nativeOperationSpec{"images.generate", "/images/generations", false, true}, true
	case "/v1/images/edits":
		return nativeOperationSpec{"images.edit", "/images/edits", true, true}, true
	case "/v1/audio/transcriptions":
		return nativeOperationSpec{"audio.transcribe", "/audio/transcriptions", true, true}, true
	case "/v1/audio/translations":
		return nativeOperationSpec{"audio.translate", "/audio/translations", true, true}, true
	case "/v1/audio/speech":
		return nativeOperationSpec{"audio.speech", "/audio/speech", false, true}, true
	case "/v1/embeddings":
		return nativeOperationSpec{"embeddings", "/embeddings", false, false}, true
	case "/v1/rerank":
		return nativeOperationSpec{"rerank", "", false, false}, true
	case "/v1/responses/compact":
		return nativeOperationSpec{"compact", "/responses/compact", false, false}, true
	default:
		return nativeOperationSpec{}, false
	}
}
func (a *App) nativeOperationsAPI(w http.ResponseWriter, r *http.Request) bool {
	op, known := nativeOperation(r.URL.Path)
	if !known {
		return false
	}
	if r.Method != "POST" {
		fail(w, 405, "方法不支持", "")
		return true
	}
	a.forwardOperation(w, r, op.Name)
	return true
}
func nativePath(src Source, op nativeOperationSpec) (string, error) {
	if src.Kind != "api_key" || !slices.Contains(src.NativeOperations, op.Name) {
		return "", errors.New("来源未明确配置此原生 operation；目录和文本测试不证明此能力")
	}
	if src.Provider != "" && src.Provider != "openai" && src.Provider != "openai_compatible" && !(op.Name == "rerank" && src.Provider == "cohere") {
		return "", errors.New("此 provider 需要独立原生认证适配，不能继承 API Bearer operation")
	}
	if op.Name != "rerank" {
		return op.Path, nil
	}
	path := src.RerankPath
	u, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || u.IsAbs() || u.Host != "" || u.RawQuery != "" || u.Fragment != "" || strings.Contains(path, "\\") || urlpath.Clean(path) != path || strings.Contains(path, "..") || strings.ContainsAny(path, "\r\n") {
		return "", errors.New("rerank 原生路径未配置或不是受控相对路径")
	}
	return path, nil
}

type nativeInput struct {
	Folder, Path, ContentType, Model string
	Bytes                            int64
	Body                             map[string]json.RawMessage
	Batch                            int
	Dimensions                       int
	Encoding                         string
	Documents                        []string
	TopN                             int
	ReturnDocuments                  bool
	Opaque                           []string
}

func nativeSpool(ctx context.Context, c Config, r *http.Request, op nativeOperationSpec, authorize func(string) error) (nativeInput, error) {
	limit := nativeJSONLimit
	if op.Media {
		limit = nativeMediaLimit
	}
	input := nativeInput{ContentType: r.Header.Get("Content-Type")}
	if r.ContentLength > limit {
		return input, &http.MaxBytesError{Limit: limit}
	}
	media, params, err := mime.ParseMediaType(input.ContentType)
	if err != nil {
		return input, errors.New("Content-Type 无效")
	}
	if !(media == "multipart/form-data" && op.Multipart) && !(media == "application/json" && op.Name != "audio.transcribe" && op.Name != "audio.translate") {
		return input, errors.New("此 operation 的请求 Content-Type 不受支持")
	}
	folder, err := os.MkdirTemp(c.DataDir, ".native-operation-")
	if err != nil {
		return input, storageError()
	}
	input.Folder = folder
	input.Path = filepath.Join(folder, "request")
	file, err := os.OpenFile(input.Path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		os.RemoveAll(folder)
		return input, storageError()
	}
	stop := context.AfterFunc(ctx, func() { r.Body.Close() })
	defer stop()
	if err = ctx.Err(); err == nil {
		// Only a bounded metadata prefix may be read before model/source authorization.
		// Buffered parser read-ahead is already written by TeeReader, so it is not replayed.
		prefix := io.LimitReader(io.TeeReader(r.Body, file), nativeAdmissionLimit+1)
		if media == "multipart/form-data" {
			input.Model, err = nativeMultipartModel(prefix, params["boundary"])
		} else {
			input.Model, err = nativeJSONModel(prefix)
		}
		input.Bytes, _ = file.Seek(0, io.SeekCurrent)
		if input.Bytes > nativeAdmissionLimit {
			err = &nativeAdmissionError{422, "model 必须位于请求前 64 KiB 元数据内，且在大媒体字段前；未读取大 body 或派发", "model"}
		}
		if err == nil {
			err = authorize(input.Model)
		}
		if err == nil {
			var remaining int64
			remaining, err = io.CopyBuffer(file, io.LimitReader(r.Body, limit-input.Bytes+1), make([]byte, 32<<10))
			input.Bytes += remaining
		}
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	if err == nil && input.Bytes > limit {
		err = &http.MaxBytesError{Limit: limit}
	}
	if err != nil {
		os.RemoveAll(folder)
		return input, err
	}
	return input, nil
}

// Scan bounded model metadata before allocating or spooling the large media envelope.
func nativeJSONModel(input io.Reader) (string, error) {
	reader := bufio.NewReaderSize(input, 32<<10)
	nonspace := func() (byte, error) {
		for {
			b, e := reader.ReadByte()
			if e != nil {
				return b, e
			}
			if b != ' ' && b != '\r' && b != '\n' && b != '\t' {
				return b, nil
			}
		}
	}
	quoted := func(max int) (string, error) {
		var raw bytes.Buffer
		raw.WriteByte('"')
		escaped := false
		for {
			b, e := reader.ReadByte()
			if e != nil {
				return "", e
			}
			raw.WriteByte(b)
			if raw.Len() > max {
				return "", errors.New("model 或字段名超过元数据上限")
			}
			if b == '"' && !escaped {
				break
			}
			if b == '\\' && !escaped {
				escaped = true
			} else {
				escaped = false
			}
		}
		var value string
		if json.Unmarshal(raw.Bytes(), &value) != nil {
			return "", errors.New("JSON 字符串无效")
		}
		return value, nil
	}
	b, err := nonspace()
	if err != nil || b != '{' {
		return "", errors.New("请求必须为 JSON 对象")
	}
	for {
		b, err = nonspace()
		if err != nil || b != '"' {
			return "", errors.New("model 缺失或请求 JSON 无效")
		}
		name, e := quoted(1024)
		if e != nil {
			return "", e
		}
		b, err = nonspace()
		if err != nil || b != ':' {
			return "", errors.New("请求 JSON 无效")
		}
		b, err = nonspace()
		if err != nil {
			return "", errors.New("请求 JSON 无效")
		}
		if name == "model" {
			if b != '"' {
				return "", errors.New("model 必须为字符串")
			}
			model, e := quoted(4096)
			if e != nil || model == "" {
				return "", errors.New("model 无效")
			}
			return model, nil
		}
		// Skip a value without materializing large base64/text strings. The authorized full decode validates its syntax later.
		depth := 0
		inString := false
		escape := false
		for {
			if inString {
				if b == '"' && !escape {
					inString = false
				}
				if b == '\\' && !escape {
					escape = true
				} else {
					escape = false
				}
			} else {
				switch b {
				case '"':
					inString = true
				case '[', '{':
					depth++
					if depth > 64 {
						return "", errors.New("JSON 嵌套超过上限")
					}
				case ']':
					depth--
				case '}':
					if depth == 0 {
						return "", errors.New("model 缺失")
					}
					depth--
				case ',':
					if depth == 0 {
						goto nextField
					}
				}
			}
			b, err = reader.ReadByte()
			if err != nil {
				return "", errors.New("model 缺失或请求 JSON 无效")
			}
		}
	nextField:
	}
}
func nativeMultipartModel(input io.Reader, boundary string) (string, error) {
	if boundary == "" {
		return "", errors.New("multipart boundary 缺失")
	}
	reader := multipart.NewReader(input, boundary)
	parts := 0
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			return "", errors.New("multipart 结构无效")
		}
		parts++
		if parts > 2048 {
			return "", errors.New("multipart part 过多")
		}
		if part.FormName() == "model" {
			if part.FileName() != "" {
				return "", errors.New("model 类型无效")
			}
			b, e := readLimited(part, 4096)
			if e != nil {
				return "", errors.New("model 超过元数据上限")
			}
			if len(b) == 0 {
				return "", errors.New("model 缺失")
			}
			return string(b), nil
		} else {
			if _, e = io.Copy(io.Discard, part); e != nil {
				return "", errors.New("multipart 读取失败")
			}
		}
		part.Close()
	}
	return "", errors.New("model 缺失")
}
func nativeResources(value any, opaque *[]string) error {
	switch v := value.(type) {
	case map[string]any:
		for k, item := range v {
			if k == "file_id" || k == "previous_response_id" || k == "conversation" || k == "vector_store_ids" {
				resource := false
				switch ref := item.(type) {
				case string:
					resource = ref != ""
				case []any:
					resource = len(ref) != 0
				case map[string]any:
					if k == "conversation" {
						_, resource = ref["id"]
					}
				}
				if resource {
					return errors.New("资源 ID 尚无此 operation 的归属适配，不能裸透传")
				}
			}
			if k == "encrypted_content" {
				if text, ok := item.(string); ok && text != "" {
					*opaque = append(*opaque, text)
				}
			}
			if err := nativeResources(item, opaque); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := nativeResources(item, opaque); err != nil {
				return err
			}
		}
	}
	return nil
}
func (in *nativeInput) prepare(op nativeOperationSpec, sentModel string) error {
	media, params, _ := mime.ParseMediaType(in.ContentType)
	if media == "multipart/form-data" {
		return in.prepareMultipart(op, params["boundary"], sentModel)
	}
	file, err := os.Open(in.Path)
	if err != nil {
		return storageError()
	}
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&in.Body)
	var extra any
	if err == nil && decoder.Decode(&extra) != io.EOF {
		err = errors.New("只接受一个 JSON 对象")
	}
	file.Close()
	if err != nil || in.Body == nil {
		return errors.New("请求 JSON 无效")
	}
	var decodedModel string
	if json.Unmarshal(in.Body["model"], &decodedModel) != nil || decodedModel != in.Model {
		return errors.New("model 在请求中重复或与已授权的元数据不一致")
	}
	var generic any
	b, _ := json.Marshal(in.Body)
	if json.Unmarshal(b, &generic) != nil {
		return errors.New("请求 JSON 无效")
	}
	if err = nativeResources(generic, &in.Opaque); err != nil {
		return err
	}
	in.Body["model"] = json.RawMessage(encode(sentModel))
	requiredText := func(field string) error {
		var text string
		if json.Unmarshal(in.Body[field], &text) != nil || text == "" {
			return fmt.Errorf("%s 必须为非空文本", field)
		}
		return nil
	}
	switch op.Name {
	case "images.generate":
		err = requiredText("prompt")
	case "images.edit":
		err = requiredText("prompt")
		if err == nil {
			var images []struct {
				URL string `json:"image_url"`
			}
			if json.Unmarshal(in.Body["images"], &images) != nil || len(images) == 0 {
				err = errors.New("JSON 图像编辑需提供 images 引用")
			} else {
				for _, image := range images {
					if e := validateNativeImageURL(image.URL); e != nil {
						err = e
						break
					}
				}
			}
			if mask, ok := in.Body["mask"]; ok && string(mask) != "null" {
				var image struct {
					URL string `json:"image_url"`
				}
				if json.Unmarshal(mask, &image) != nil {
					err = errors.New("mask 图片引用无效")
				} else if e := validateNativeImageURL(image.URL); e != nil {
					err = e
				}
			}
		}
	case "audio.speech":
		err = requiredText("input")
		if err == nil {
			if _, ok := in.Body["voice"]; !ok {
				err = errors.New("voice 缺失")
			}
		}
	case "embeddings":
		in.Batch, err = embeddingInputCount(in.Body["input"])
		if err == nil {
			if value, ok := in.Body["dimensions"]; ok && (json.Unmarshal(value, &in.Dimensions) != nil || in.Dimensions < 1) {
				err = errors.New("dimensions 必须为正整数")
			}
			in.Encoding = "float"
			if value, ok := in.Body["encoding_format"]; ok && (json.Unmarshal(value, &in.Encoding) != nil || (in.Encoding != "float" && in.Encoding != "base64")) {
				err = errors.New("encoding_format 无效")
			}
		}
	case "rerank":
		err = requiredText("query")
		if err == nil && (json.Unmarshal(in.Body["documents"], &in.Documents) != nil || len(in.Documents) == 0 || len(in.Documents) > 2048) {
			err = errors.New("documents 必须为 1 到 2048 个字符串")
		}
		in.TopN = len(in.Documents)
		if value, ok := in.Body["top_n"]; ok && (json.Unmarshal(value, &in.TopN) != nil || in.TopN < 1 || in.TopN > len(in.Documents)) {
			err = errors.New("top_n 超过 documents 边界")
		}
		if value, ok := in.Body["return_documents"]; ok && json.Unmarshal(value, &in.ReturnDocuments) != nil {
			err = errors.New("return_documents 必须为布尔值")
		}
		delete(in.Body, "return_documents")
	case "compact":
		if _, ok := in.Body["input"]; !ok {
			err = errors.New("compact input 缺失")
		}
	}
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(in.Body)
	if err != nil {
		return errors.New("请求 JSON 无效")
	}
	return os.WriteFile(in.Path, encoded, 0600)
}
func validateNativeImageURL(raw string) error {
	if strings.HasPrefix(raw, "data:") {
		comma := strings.IndexByte(raw, ',')
		if comma < 0 || !strings.HasSuffix(raw[:comma], ";base64") {
			return errors.New("图片 data URL 格式无效")
		}
		kind := strings.TrimSuffix(strings.TrimPrefix(raw[:comma], "data:"), ";base64")
		decoder := base64.NewDecoder(base64.StdEncoding, strings.NewReader(raw[comma+1:]))
		prefix := make([]byte, 512)
		n, err := io.ReadFull(decoder, prefix)
		if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
			return errors.New("图片 base64 无效")
		}
		count, err := io.Copy(io.Discard, io.LimitReader(decoder, nativeMediaLimit+1))
		if err != nil || int64(n)+count > nativeMediaLimit {
			return errors.New("图片 base64 无效或解码超过 32 MiB")
		}
		return validateNativeMedia(prefix[:n], kind, false)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return errors.New("图片引用需为 HTTPS URL 或合法 base64 data URL")
	}
	return nil
}
func validateNativeMedia(prefix []byte, declared string, audio bool) error {
	detected := http.DetectContentType(prefix)
	if audio {
		known := detected == "audio/mpeg" || detected == "audio/wave" || detected == "audio/aiff" || detected == "audio/basic" || detected == "application/ogg" || detected == "video/webm" || bytes.HasPrefix(prefix, []byte("fLaC")) || bytes.HasPrefix(prefix, []byte("ID3")) || len(prefix) > 8 && string(prefix[4:8]) == "ftyp"
		if !known {
			return errors.New("音频文件 magic 不受支持")
		}
	} else if detected != "image/png" && detected != "image/jpeg" && detected != "image/webp" {
		return errors.New("图片文件 magic 不受支持")
	}
	declared, _, err := mime.ParseMediaType(declared)
	if err != nil {
		return errors.New("媒体 MIME 无效")
	}
	if declared != "application/octet-stream" && declared != detected && !(audio && (declared == "audio/wav" && detected == "audio/wave" || declared == "audio/ogg" && detected == "application/ogg" || declared == "audio/flac" && bytes.HasPrefix(prefix, []byte("fLaC")) || declared == "audio/mp4" && len(prefix) > 8 && string(prefix[4:8]) == "ftyp")) {
		return errors.New("媒体 MIME 与文件 magic 不符")
	}
	return nil
}
func (in *nativeInput) prepareMultipart(op nativeOperationSpec, boundary, sentModel string) error {
	source, err := os.Open(in.Path)
	if err != nil {
		return storageError()
	}
	defer source.Close()
	target := filepath.Join(in.Folder, "prepared")
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
	if err != nil {
		return storageError()
	}
	writer := multipart.NewWriter(file)
	reader := multipart.NewReader(source, boundary)
	images, files, models, parts := 0, 0, 0, 0
	in.Body = map[string]json.RawMessage{}
	for {
		part, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil {
			err = errors.New("multipart 结构无效")
			break
		}
		parts++
		if parts > 2048 {
			err = errors.New("multipart part 过多")
			break
		}
		name := part.FormName()
		header := textproto.MIMEHeader{}
		for k, v := range part.Header {
			header[k] = append([]string{}, v...)
		}
		if name == "file_id" {
			err = errors.New("file_id 尚无归属适配")
			break
		}
		output, e := writer.CreatePart(header)
		if e != nil {
			err = storageError()
			break
		}
		if part.FileName() != "" {
			if name == "model" {
				err = errors.New("model 类型无效")
				break
			}
			isAudio := op.Name == "audio.transcribe" || op.Name == "audio.translate"
			if isAudio && name != "file" || !isAudio && (name != "image" && name != "image[]" && name != "mask") {
				err = errors.New("此 multipart 文件字段不受支持")
				break
			}
			buffered := bufio.NewReader(part)
			prefix, _ := buffered.Peek(512)
			if e = validateNativeMedia(prefix, part.Header.Get("Content-Type"), isAudio); e != nil {
				err = e
				break
			}
			if _, e = io.Copy(output, buffered); e != nil {
				err = storageError()
				break
			}
			files++
			if name == "image" || name == "image[]" {
				images++
			}
		} else {
			value, e := readLimited(part, 1<<20)
			if e != nil {
				err = errors.New("multipart 文本字段超过上限")
				break
			}
			if name == "model" {
				models++
				if models != 1 || string(value) != in.Model {
					err = errors.New("model 重复或与已授权的元数据不一致")
					break
				}
				value = []byte(sentModel)
			}
			if _, e = output.Write(value); e != nil {
				err = storageError()
				break
			}
			in.Body[name] = json.RawMessage(encode(string(value)))
		}
		part.Close()
	}
	if e := writer.Close(); err == nil {
		err = e
	}
	if e := file.Close(); err == nil {
		err = e
	}
	if err != nil {
		os.Remove(target)
		return err
	}
	if op.Name == "images.edit" && (images == 0 || string(in.Body["prompt"]) == "\"\"" || in.Body["prompt"] == nil) {
		return errors.New("图像编辑需 image 与 prompt")
	}
	if op.Name != "images.edit" && files != 1 {
		return errors.New("转录和翻译需一个音频 file")
	}
	in.Path = target
	in.ContentType = writer.FormDataContentType()
	return nil
}
func embeddingInputCount(raw json.RawMessage) (int, error) {
	var input any
	if json.Unmarshal(raw, &input) != nil {
		return 0, errors.New("embedding input 无效")
	}
	switch v := input.(type) {
	case string:
		if v == "" {
			return 0, errors.New("embedding input 不能为空")
		}
		return 1, nil
	case []any:
		if len(v) == 0 {
			return 0, errors.New("embedding input 不能为空")
		}
		if _, ok := v[0].(float64); ok {
			for _, x := range v {
				n, ok := x.(float64)
				if !ok || n < 0 || n != math.Trunc(n) {
					return 0, errors.New("embedding token 数组无效")
				}
			}
			return 1, nil
		}
		if len(v) > 2048 {
			return 0, &http.MaxBytesError{Limit: 2048}
		}
		for _, x := range v {
			switch item := x.(type) {
			case string:
				if item == "" {
					return 0, errors.New("embedding 文本不能为空")
				}
			case []any:
				if len(item) == 0 {
					return 0, errors.New("embedding token 数组不能为空")
				}
				for _, n := range item {
					f, ok := n.(float64)
					if !ok || f < 0 || f != math.Trunc(f) {
						return 0, errors.New("embedding token 数组无效")
					}
				}
			default:
				return 0, errors.New("embedding input 类型无效")
			}
		}
		return len(v), nil
	}
	return 0, errors.New("embedding input 类型无效")
}

func opaqueBinding(content string, src Source) string {
	return "opaque_" + digest(strconv.Itoa(src.AccountGeneration)+"\x00"+content)
}
func (a *App) nativeSelectSource(key ClientKey, model string, op nativeOperationSpec) (Source, string, []CandidateReason, error) {
	excluded := map[string]bool{}
	if key.RouteID != "" {
		route, err := a.Store.route(key.RouteID)
		if err != nil {
			return Source{}, "", nil, err
		}
		for _, member := range route.Members {
			m, err := a.Store.model(member.ModelID)
			if err != nil {
				continue
			}
			src, err := a.Store.source(m.SourceID)
			if err != nil {
				continue
			}
			if _, err = nativePath(src, op); err != nil {
				excluded[src.ID] = true
			}
		}
	}
	src, sent, reasons, err := a.selectSourceExcluding(key, model, "responses", false, excluded)
	if err != nil {
		return src, sent, reasons, err
	}
	if _, err = nativePath(src, op); err != nil {
		return src, sent, reasons, err
	}
	if src.Deleted || !src.Enabled || !src.Configured || src.AuthStatus == "needs_reauth" || src.AuthStatus == "logged_out" || src.AuthStatus == "rejected" {
		return src, sent, reasons, errors.New("所选来源未就绪或需要重新认证")
	}
	return src, sent, reasons, nil
}
func (a *App) nativeAccounting(key ClientKey, src Source, input nativeInput, op nativeOperationSpec) (*AccountingPlan, error) {
	if op.Name == "compact" {
		return a.prepareAccounting(key, src, input.Body)
	}
	return a.prepareNativeAccounting(key, src, input, op)
}

func (a *App) forwardOperation(w http.ResponseWriter, r *http.Request, operation string) {
	op, known := nativeOperation(r.URL.Path)
	if !known || op.Name != operation {
		fail(w, 404, "原生 operation 不存在", "")
		return
	}
	requestID := id("req")
	w.Header().Set("X-Gateway-Request-Id", requestID)
	a.mu.Lock()
	c := a.Config
	if a.stopping || a.storageFailed.Load() {
		a.mu.Unlock()
		fail(w, 503, "服务正在关闭或存储异常", "")
		return
	}
	key, err := a.Store.keyByDigest(digest(bearer(r)))
	if err != nil || !keyValid(key, time.Now()) {
		a.mu.Unlock()
		fail(w, 401, "客户端 Key 无效或已撤销", "")
		return
	}
	if !allowed(key.ProtocolAllowlist, "responses") || !allowed(key.OperationAllowlist, op.Name) {
		a.mu.Unlock()
		fail(w, 403, "Key 无此原生协议或操作权限", "operation")
		return
	}
	if key.Limits.MaxConcurrent != nil && a.keyActive[key.ID] >= *key.Limits.MaxConcurrent || len(a.slots) >= c.MaxConcurrent {
		a.mu.Unlock()
		fail(w, 429, "本机或 Key 并发容量已满", "")
		return
	}
	select {
	case a.slots <- struct{}{}:
		a.keyActive[key.ID]++
		a.mu.Unlock()
	default:
		a.mu.Unlock()
		fail(w, 429, "本机并发容量已满", "")
		return
	}
	defer func() { a.mu.Lock(); <-a.slots; a.keyActive[key.ID]--; a.mu.Unlock() }()
	ctx, cancel := context.WithTimeout(r.Context(), time.Duration(c.TotalTimeout)*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	_ = controller.SetReadDeadline(time.Now().Add(time.Duration(c.HeaderTimeout) * time.Second))
	var src Source
	var sent string
	var reasons []CandidateReason
	input, err := nativeSpool(ctx, c, r, op, func(model string) error {
		a.mu.Lock()
		defer a.mu.Unlock()
		current, lookupErr := a.Store.keyByDigest(digest(bearer(r)))
		if lookupErr != nil || !keyValid(current, time.Now()) || current.Version != key.Version {
			return &nativeAdmissionError{409, "Key 已修改或撤销，请重新发送", ""}
		}
		if !allowed(key.ModelAllowlist, model) {
			return &nativeAdmissionError{403, "Key 无此模型权限", "model"}
		}
		var selectErr error
		src, sent, reasons, selectErr = a.nativeSelectSource(key, model, op)
		if selectErr != nil {
			return &nativeAdmissionError{422, selectErr.Error(), "operation"}
		}
		if a.stopping || a.storageFailed.Load() {
			return &nativeAdmissionError{503, "服务正在关闭或存储异常", ""}
		}
		return nil
	})
	_ = controller.SetReadDeadline(time.Time{})
	if err != nil {
		code := 400
		message, field := "原生请求暂存失败、超限或超时；未派发", ""
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			code = 413
		}
		if ctx.Err() != nil {
			code = 408
		}
		var admission *nativeAdmissionError
		if errors.As(err, &admission) {
			code, message, field = admission.Code, admission.Message, admission.Field
		}
		fail(w, code, message, field)
		return
	}
	defer os.RemoveAll(input.Folder)
	a.mu.Lock()
	reject := func(code int, message, field string) { a.mu.Unlock(); fail(w, code, message, field) }
	current, err := a.Store.keyByDigest(digest(bearer(r)))
	if err != nil || !keyValid(current, time.Now()) || current.Version != key.Version {
		reject(409, "Key 已修改或撤销，请重新发送", "")
		return
	}
	key = current
	if !allowed(key.ModelAllowlist, input.Model) {
		reject(403, "Key 无此模型权限", "model")
		return
	}
	if a.stopping || a.storageFailed.Load() {
		reject(503, "服务正在关闭或存储异常", "")
		return
	}
	if src.MaxConcurrent != nil && a.accountActive[src.AccountID] >= *src.MaxConcurrent {
		reject(429, "账号并发已满", "")
		return
	}
	if key.RouteID != "" {
		route, e := a.Store.route(key.RouteID)
		if e != nil {
			reject(503, storageError().Error(), "")
			return
		}
		if route.MaxConcurrent != nil && a.routeActive[route.ID] >= *route.MaxConcurrent {
			reject(429, "路由并发已满", "")
			return
		}
	}
	// Model and operation authorization precede the full media JSON allocation and multipart transformation.
	a.mu.Unlock()
	if err = input.prepare(op, sent); err != nil {
		code := 422
		var large *http.MaxBytesError
		if errors.As(err, &large) {
			code = 413
		}
		fail(w, code, err.Error(), "")
		return
	}
	a.mu.Lock()
	current, err = a.Store.keyByDigest(digest(bearer(r)))
	if err != nil || current.Version != key.Version || !keyValid(current, time.Now()) {
		reject(409, "Key 已改变，请重新发送", "")
		return
	}
	latest, err := a.Store.source(src.ID)
	if err != nil || latest.Version != src.Version || latest.Generation != src.Generation || latest.AccountGeneration != src.AccountGeneration || !latest.Enabled || latest.Deleted {
		reject(409, "来源或凭据代次已改变，请重新发送", "")
		return
	}
	if a.stopping || a.storageFailed.Load() {
		reject(503, "服务正在关闭或存储异常", "")
		return
	}
	if src.MaxConcurrent != nil && a.accountActive[src.AccountID] >= *src.MaxConcurrent {
		reject(429, "账号并发已满", "")
		return
	}
	if key.RouteID != "" {
		route, routeErr := a.Store.route(key.RouteID)
		if routeErr != nil || !route.Enabled {
			reject(409, "路由已改变或停用，请重新发送", "target")
			return
		}
		if route.MaxConcurrent != nil && a.routeActive[route.ID] >= *route.MaxConcurrent {
			reject(429, "路由并发已满", "")
			return
		}
	}
	for _, opaque := range input.Opaque {
		if op.Name != "compact" || !a.Store.continuation(opaqueBinding(opaque, src), key, src, sent) {
			reject(409, "opaque 历史不属于当前 Key、账号代次、来源与模型", "input")
			return
		}
	}
	var tokens int64
	estimateKind := "not_applicable"
	if op.Name == "compact" || op.Name == "embeddings" {
		raw, _ := json.Marshal(input.Body)
		tokens, estimateKind, err = reservationEstimate(input.Body, raw)
		if err != nil && key.Limits.TPM != nil {
			reject(422, "当前输入无法按 token 窗口预留", "limits.tpm")
			return
		}
	} else if key.Limits.TPM != nil {
		reject(422, "媒体或重排 token 维度未知，不能绕过此 Key 的 token 限额", "limits.tpm")
		return
	}
	if ok, retry := a.checkTPM(key, tokens, time.Now()); !ok {
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
		}
		reject(429, "Key token 窗口已满", "limits.tpm")
		return
	}
	accounting, err := a.nativeAccounting(key, src, input, op)
	if err != nil {
		a.mu.Unlock()
		accountingFailure(w, err)
		return
	}
	secret, err := a.Secrets.Get(src.CredentialRef)
	if err != nil {
		reject(503, "来源凭据不可用，请重新配置", "")
		return
	}
	if ok, retry := a.consumeRPM(key, time.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		reject(429, "Key 请求速率已满", "limits.rpm")
		return
	}
	now := time.Now().UTC()
	record := Record{ID: requestID, Protocol: "responses", Operation: op.Name, Origin: "client", KeyID: key.ID, SourceID: src.ID, AccountID: src.AccountID, AccountGeneration: src.AccountGeneration, Generation: src.Generation, RouteID: key.RouteID, Model: input.Model, SentModel: sent, Status: "dispatching", UpstreamStatus: "unknown", DeliveryStatus: "not_started", ObservationStatus: "complete", Started: now, AttemptStarted: now, AttemptID: id("att"), Sequence: 1, Version: 1, Submission: "possible", Completeness: "unknown", Accounting: accounting, ReservedTokens: tokens, TokenReservationSource: estimateKind, SelectionReasons: reasons, RequestBytes: input.Bytes}
	record.TraceParent = r.Header.Get("Traceparent")
	record.Price = src.Price
	if err = a.Store.record(record); err != nil {
		a.refundRPM(key)
		var budgetErr *accountingError
		if !errors.As(err, &budgetErr) {
			a.markStorageFailure()
		}
		a.mu.Unlock()
		accountingFailure(w, err)
		return
	}
	a.reserveTPM(key, record.ID, tokens, now)
	a.running[record.ID] = cancel
	a.runningSources[record.ID] = src.ID
	a.runningKeys[record.ID] = key.ID
	a.accountActive[src.AccountID]++
	if key.RouteID != "" {
		a.routeActive[key.RouteID]++
	}
	a.mu.Unlock()
	defer func() {
		ended := time.Now().UTC()
		record.Ended = &ended
		record.DurationMS = ended.Sub(record.Started).Milliseconds()
		record.Version++
		if ctx.Err() != nil && record.Status != "succeeded" {
			record.Status = "cancelled"
			record.ErrorStage = "cancelled"
			record.DeliveryStatus = "failed"
		}
		if record.Status == "dispatching" || record.Status == "streaming" {
			record.Status = "failed"
			record.ErrorStage = "incomplete"
		}
		record.Completeness = usageCompleteness(record.Usage)
		if record.ObservationStatus == "partial" && record.Completeness == "complete" {
			record.Completeness = "partial"
		}
		if record.Submission == "not_sent" {
			zero := "0"
			record.Cost = &zero
		} else if op.Name == "compact" {
			record.Cost = estimate(record.Usage, record.Price)
			if record.Cost == nil {
				record.PartialCost = estimatePartial(record.Usage, record.Price)
			}
		}
		finalizeNativeCost(&record, op)
		record.Completeness = usageCompleteness(record.Usage)
		if err := a.Store.record(record); err != nil {
			a.markStorageFailure()
		} else {
			a.observeExecution(record, src, true)
		}
		a.mu.Lock()
		a.settleTPM(key, record)
		delete(a.running, record.ID)
		delete(a.runningSources, record.ID)
		delete(a.runningKeys, record.ID)
		a.accountActive[src.AccountID]--
		if key.RouteID != "" {
			a.routeActive[key.RouteID]--
		}
		a.mu.Unlock()
	}()
	path, err := nativePath(src, op)
	if err != nil {
		record.ErrorStage = "prepare"
		record.Submission = "not_sent"
		fail(w, 422, err.Error(), "operation")
		return
	}
	file, err := os.Open(input.Path)
	if err != nil {
		record.ErrorStage = "prepare"
		record.Submission = "not_sent"
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer file.Close()
	req, err := http.NewRequestWithContext(ctx, "POST", safeEndpoint(src.BaseURL, path), file)
	if err != nil {
		record.ErrorStage = "prepare"
		record.Submission = "not_sent"
		fail(w, 502, "原生 endpoint 无效", "")
		return
	}
	info, err := file.Stat()
	if err != nil {
		record.ErrorStage = "prepare"
		record.Submission = "not_sent"
		fail(w, 503, storageError().Error(), "")
		return
	}
	req.ContentLength = info.Size()
	req.GetBody = nil
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Content-Type", input.ContentType)
	req.Header.Set("Accept", "*/*")
	record.Submission = "possible"
	// Integration hook: root may replace this call with a.doUpstream(req,src) for the selected network policy.
	response, err := a.doUpstream(req, src)
	if err != nil {
		record.Status = "failed"
		record.ErrorStage = "upstream_transport"
		fail(w, 502, "原生上游连接失败；执行与费用可能未知，未重试", "")
		return
	}
	defer response.Body.Close()
	response.Body = &observedResponseBody{ReadCloser: response.Body, count: &record.UpstreamBytes}
	record.HTTPStatus = response.StatusCode
	record.ResponseMIME = response.Header.Get("Content-Type")
	record.UpstreamRequestID = response.Header.Get("X-Request-ID")
	stop := context.AfterFunc(ctx, func() { response.Body.Close() })
	defer stop()
	for _, header := range []string{"Retry-After", "OpenAI-Processing-Ms", "X-Request-ID"} {
		if value := response.Header.Get(header); value != "" {
			w.Header().Set(header, value)
		}
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		record.ErrorStage = "redirect"
		fail(w, 502, "上游重定向被拒绝，未向新地址发送凭据", "")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		record.Status = "failed"
		record.UpstreamStatus = "rejected"
		record.ErrorStage = "upstream_http"
		b, e := readLimited(response.Body, 1<<20)
		if e != nil {
			fail(w, 502, "上游错误响应超限或不完整", "")
			return
		}
		w.Header().Set("Content-Type", record.ResponseMIME)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(response.StatusCode)
		written, e := w.Write([]byte(redact(string(b), secret, bearer(r))))
		record.ResponseBytes = int64(written)
		if e != nil {
			record.DeliveryStatus = "failed"
		} else {
			record.DeliveryStatus = "completed"
		}
		return
	}
	a.deliverNativeOperation(w, r, response, src, key, input, op, &record, c, cancel)
}

func observeNativeUsage(record *Record, payload []byte) {
	var v map[string]json.RawMessage
	if json.Unmarshal(payload, &v) != nil {
		return
	}
	if nested, ok := v["response"]; ok {
		var inner map[string]json.RawMessage
		if json.Unmarshal(nested, &inner) == nil {
			v = inner
		}
	}
	if usage, ok := v["usage"]; ok {
		mergeUsage(&record.Usage, usage)
		var more struct {
			Prompt *int64 `json:"prompt_tokens"`
		}
		if json.Unmarshal(usage, &more) == nil && more.Prompt != nil && *more.Prompt >= 0 {
			record.Usage.Input = more.Prompt
		}
	}
	if model, ok := v["model"]; ok {
		json.Unmarshal(model, &record.ReportedModel)
	}
}
func validateEmbeddingOutput(raw []byte, input nativeInput) error {
	var envelope struct {
		Data []struct {
			Index  *int            `json:"index"`
			Vector json.RawMessage `json:"embedding"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Data) != input.Batch {
		return errors.New("embedding 返回项未覆盖输入")
	}
	seen := map[int]bool{}
	dimension := 0
	for _, item := range envelope.Data {
		if item.Index == nil || *item.Index < 0 || *item.Index >= input.Batch || seen[*item.Index] {
			return errors.New("embedding index 越界或重复")
		}
		seen[*item.Index] = true
		size := 0
		if input.Encoding == "base64" {
			var encoded string
			if json.Unmarshal(item.Vector, &encoded) != nil {
				return errors.New("embedding 编码类型不符")
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(decoded) == 0 || len(decoded)%4 != 0 {
				return errors.New("embedding base64 或 float32 长度无效")
			}
			size = len(decoded) / 4
			for i := 0; i < len(decoded); i += 4 {
				f := math.Float32frombits(binary.LittleEndian.Uint32(decoded[i : i+4]))
				if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
					return errors.New("embedding 含非有限值")
				}
			}
		} else {
			var vector []float64
			if json.Unmarshal(item.Vector, &vector) != nil || len(vector) == 0 {
				return errors.New("embedding 向量无效")
			}
			size = len(vector)
			for _, f := range vector {
				if math.IsNaN(f) || math.IsInf(f, 0) {
					return errors.New("embedding 含非有限值")
				}
			}
		}
		if dimension == 0 {
			dimension = size
		}
		if dimension != size || input.Dimensions > 0 && input.Dimensions != size {
			return errors.New("embedding 维度不一致")
		}
	}
	return nil
}
func nativeRerankOutput(raw []byte, input nativeInput, model string, record *Record) ([]byte, error) {
	var envelope struct {
		ID      string `json:"id"`
		Results []struct {
			Index *int     `json:"index"`
			Score *float64 `json:"relevance_score"`
		} `json:"results"`
		Usage struct {
			Input *int64 `json:"input_tokens"`
		} `json:"usage"`
		Meta struct {
			Tokens struct {
				Input *int64 `json:"input_tokens"`
			} `json:"tokens"`
		} `json:"meta"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Results) == 0 || len(envelope.Results) > input.TopN {
		return nil, errors.New("rerank 返回结果数量无效")
	}
	seen := map[int]bool{}
	for _, item := range envelope.Results {
		if item.Index == nil || item.Score == nil || *item.Index < 0 || *item.Index >= len(input.Documents) || seen[*item.Index] || math.IsNaN(*item.Score) || math.IsInf(*item.Score, 0) {
			return nil, errors.New("rerank 返回非法 score 或 index")
		}
		seen[*item.Index] = true
	}
	sort.SliceStable(envelope.Results, func(i, j int) bool {
		left, right := envelope.Results[i], envelope.Results[j]
		if *left.Score == *right.Score {
			return *left.Index < *right.Index
		}
		return *left.Score > *right.Score
	})
	result := []map[string]any{}
	for _, item := range envelope.Results {
		row := map[string]any{"index": *item.Index, "relevance_score": *item.Score}
		if input.ReturnDocuments {
			row["document"] = input.Documents[*item.Index]
		}
		result = append(result, row)
	}
	tokens := envelope.Usage.Input
	if tokens == nil {
		tokens = envelope.Meta.Tokens.Input
	}
	if tokens != nil && *tokens < 0 {
		tokens = nil
	}
	record.Usage.Input = tokens
	provenance := "unknown"
	if tokens != nil {
		provenance = "provider_reported"
	}
	return json.Marshal(map[string]any{"id": envelope.ID, "model": model, "results": result, "usage": map[string]any{"input_tokens": tokens, "provenance": provenance}})
}
func (a *App) bindCompactOutput(raw []byte, record Record, src Source) (bool, error) {
	var envelope struct {
		Output []map[string]json.RawMessage `json:"output"`
	}
	if json.Unmarshal(raw, &envelope) != nil || len(envelope.Output) == 0 {
		return false, errors.New("compact 返回缺失完整 output 窗口")
	}
	hasCompaction := false
	var opaqueItems []string
	for _, item := range envelope.Output {
		var kind, opaque string
		json.Unmarshal(item["type"], &kind)
		json.Unmarshal(item["encrypted_content"], &opaque)
		if kind == "compaction" {
			if opaque == "" {
				return false, errors.New("compact item 缺少 opaque 内容")
			}
			hasCompaction = true
		}
		if opaque != "" {
			opaqueItems = append(opaqueItems, opaque)
		}
	}
	if !hasCompaction {
		return false, errors.New("compact 返回没有原生 compaction item")
	}
	for _, opaque := range opaqueItems {
		binding := record
		binding.Model = record.SentModel
		if err := a.Store.bind(binding, opaqueBinding(opaque, src)); err != nil {
			a.markStorageFailure()
			return false, storageError()
		}
	}
	return true, nil
}
func (a *App) deliverNativeOperation(w http.ResponseWriter, r *http.Request, response *http.Response, src Source, key ClientKey, input nativeInput, op nativeOperationSpec, record *Record, c Config, cancel context.CancelFunc) {
	media, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		record.ErrorStage = "response_mime"
		fail(w, 502, "原生上游 Content-Type 无效", "")
		return
	}
	idle := time.AfterFunc(time.Duration(c.IdleTimeout)*time.Second, cancel)
	defer idle.Stop()
	write := func(raw []byte) error {
		_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(time.Duration(c.IdleTimeout) * time.Second))
		n, e := w.Write(raw)
		record.ResponseBytes += int64(n)
		if e != nil || n != len(raw) {
			record.DeliveryStatus = "failed"
			record.ErrorStage = "downstream_write"
			if e == nil {
				e = io.ErrShortWrite
			}
			return e
		}
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	}
	w.Header().Set("Cache-Control", "no-store")
	if media == "text/event-stream" {
		if op.Name == "embeddings" || op.Name == "rerank" || op.Name == "compact" {
			record.ErrorStage = "response_mime"
			fail(w, 502, "此原生 operation 不接受此 SSE wire", "")
			return
		}
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		record.Status = "streaming"
		terminal := false
		upstreamFailed := false
		observer := newSSE(c.MaxEvent, func(frame []byte) error {
			payload, event := sseData(frame)
			observeNativeUsage(record, payload)
			if event == "image_generation.completed" || event == "image_edit.completed" || event == "transcript.text.done" || event == "speech.audio.done" {
				terminal = true
				record.UpstreamStatus = "completed"
			}
			if event == "error" || strings.HasSuffix(event, ".failed") {
				upstreamFailed = true
				record.UpstreamStatus = "failed"
			}
			return write(frame)
		}, write)
		buffer := make([]byte, 32<<10)
		for {
			n, e := response.Body.Read(buffer)
			if n > 0 {
				idle.Reset(time.Duration(c.IdleTimeout) * time.Second)
				if err = observer.Feed(buffer[:n]); err != nil {
					record.Status = "failed"
					return
				}
			}
			if e != nil {
				if e != io.EOF {
					record.Status = "failed"
					record.ErrorStage = "upstream_stream"
					record.DeliveryStatus = "failed"
					return
				}
				break
			}
		}
		if err = observer.End(); err != nil {
			record.Status = "failed"
			return
		}
		if observer.Skipped {
			record.ObservationStatus = "partial"
		}
		if !terminal || upstreamFailed {
			record.Status = "failed"
			record.ErrorStage = "upstream_event"
			record.DeliveryStatus = "completed"
			return
		}
		record.Status = "succeeded"
		record.DeliveryStatus = "completed"
		return
	}
	// Binary and subtitle bodies are staged once to avoid presenting an oversized/truncated result as a complete file.
	limit := c.MaxResponse
	if op.Media {
		limit = nativeMediaLimit
	}
	path := filepath.Join(input.Folder, "response")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_EXCL, 0600)
	if err != nil {
		record.ErrorStage = "response_store"
		fail(w, 503, storageError().Error(), "")
		return
	}
	defer file.Close()
	n, err := io.CopyBuffer(file, io.LimitReader(&nativeIdleReader{Reader: response.Body, Timer: idle, Idle: time.Duration(c.IdleTimeout) * time.Second}, limit+1), make([]byte, 32<<10))
	if err != nil || n > limit {
		record.Status = "failed"
		record.ErrorStage = "response_limit"
		if err != nil {
			record.ErrorStage = "response_body"
		}
		fail(w, 502, "原生响应超限、断流或超时；已派发，费用可能未知，未重试", "")
		return
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		record.ErrorStage = "response_store"
		fail(w, 503, storageError().Error(), "")
		return
	}
	binaryOutput := op.Name == "audio.speech"
	subtitleOutput := op.Name == "audio.transcribe" || op.Name == "audio.translate"
	if binaryOutput {
		if !strings.HasPrefix(media, "audio/") && media != "application/octet-stream" {
			record.ErrorStage = "response_mime"
			fail(w, 502, "语音输出不是原生音频 MIME", "")
			return
		}
	}
	if !binaryOutput && (!subtitleOutput || media == "application/json") {
		raw, err := io.ReadAll(file)
		if err != nil || !json.Valid(raw) {
			record.ErrorStage = "response_body"
			fail(w, 502, "原生响应不是有效 JSON", "")
			return
		}
		observeNativeUsage(record, raw)
		if op.Name == "embeddings" {
			err = validateEmbeddingOutput(raw, input)
		}
		if op.Name == "rerank" && err == nil {
			raw, err = nativeRerankOutput(raw, input, record.Model, record)
		}
		if op.Name == "compact" && err == nil {
			_, err = a.bindCompactOutput(raw, *record, src)
		}
		if (op.Name == "images.generate" || op.Name == "images.edit") && err == nil {
			var body struct {
				Data []json.RawMessage `json:"data"`
			}
			if json.Unmarshal(raw, &body) != nil || len(body.Data) == 0 {
				err = errors.New("图像响应缺少原生 data")
			} else {
				record.UsageDimensions = map[string]string{"image": strconv.Itoa(len(body.Data))}
			}
		}
		if err != nil {
			record.Status = "failed"
			record.ErrorStage = "response_validation"
			fail(w, 502, err.Error(), "")
			return
		}
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		if err = write(raw); err != nil {
			record.Status = "failed"
			return
		}
	} else {
		if subtitleOutput && media != "text/plain" && media != "text/vtt" && media != "application/x-subrip" && media != "text/srt" {
			record.ErrorStage = "response_mime"
			fail(w, 502, "转录输出 MIME 不受支持，未伪装成 JSON", "")
			return
		}
		w.Header().Set("Content-Type", response.Header.Get("Content-Type"))
		w.WriteHeader(response.StatusCode)
		buffer := make([]byte, 32<<10)
		for {
			n, e := file.Read(buffer)
			if n > 0 {
				if err = write(buffer[:n]); err != nil {
					record.Status = "failed"
					return
				}
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				record.Status = "failed"
				record.ErrorStage = "response_store"
				return
			}
		}
	}
	record.Status = "succeeded"
	record.UpstreamStatus = "completed"
	record.DeliveryStatus = "completed"
}

type nativeIdleReader struct {
	Reader io.Reader
	Timer  *time.Timer
	Idle   time.Duration
}

func (r *nativeIdleReader) Read(b []byte) (int, error) {
	n, e := r.Reader.Read(b)
	if n > 0 {
		r.Timer.Reset(r.Idle)
	}
	return n, e
}

// Hook for the ordinary native Responses ingress after source/model selection.
// Only input history is inspected; tool schemas and user text are not treated as resource authority.
func (a *App) validateNativeOpaqueHistory(body map[string]json.RawMessage, key ClientKey, src Source, model string) error {
	raw, ok := body["input"]
	if !ok {
		return nil
	}
	var input any
	if json.Unmarshal(raw, &input) != nil {
		return errors.New("input 结构无效")
	}
	var opaque []string
	var collect func(any)
	collect = func(value any) {
		switch item := value.(type) {
		case map[string]any:
			if src.Kind == "codex_subscription" {
				if kind, _ := item["type"].(string); kind != "compaction" {
					for name, child := range item {
						if name != "encrypted_content" {
							collect(child)
						}
					}
					return
				}
			}
			for name, child := range item {
				if name == "encrypted_content" {
					if content, ok := child.(string); ok && content != "" {
						opaque = append(opaque, content)
					}
				}
				collect(child)
			}
		case []any:
			for _, child := range item {
				collect(child)
			}
		}
	}
	collect(input)
	if len(opaque) == 0 {
		return nil
	}
	if !slices.Contains(src.NativeOperations, "compact") {
		return errors.New("来源未明确支持 opaque compact 历史")
	}
	for _, content := range opaque {
		if !a.Store.continuation(opaqueBinding(content, src), key, src, model) {
			return errors.New("opaque 历史不属于当前 Key、来源、账号代次与模型")
		}
	}
	return nil
}
