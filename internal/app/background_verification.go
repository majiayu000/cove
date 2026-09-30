package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"time"
)

// This capability is carried only by an internal context, never by a public
// header or request body. It selects an existing Key rather than minting one.
type resourceVerificationContextKey struct{}
type resourceVerificationContext struct {
	KeyID        string
	ModelID      string
	ModelVersion int
	SourceID     string
}

func resourceVerificationOrigin(ctx context.Context) string {
	if v, ok := ctx.Value(resourceVerificationContextKey{}).(resourceVerificationContext); ok && v.KeyID != "" {
		return "verification"
	}
	return "client"
}
func (a *App) resourceCurrentKey(r *http.Request) (ClientKey, error) {
	v, ok := r.Context().Value(resourceVerificationContextKey{}).(resourceVerificationContext)
	if !ok {
		return a.Store.keyByDigest(digest(bearer(r)))
	}
	var key ClientKey
	var raw string
	if err := a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", v.KeyID).Scan(&raw); err != nil {
		return key, err
	}
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return key, err
	}
	if key.ID != v.KeyID {
		return key, &resourceError{409, "后台验证 Key 归属不一致", "client_key_id"}
	}
	model, err := a.Store.model(v.ModelID)
	if err != nil || !model.Enabled || model.Version != v.ModelVersion || model.SourceID != v.SourceID {
		return key, &resourceError{409, "后台验证模型在准备期间已改变", "model"}
	}
	return key, nil
}
func validateBackgroundVerification(src Source, model SourceModel, key ClientKey) error {
	if src.Kind != "api_key" || src.NativeProtocol != "responses" || (src.Provider != "openai" && src.Provider != "openai_compatible" && src.Provider != "") || !slices.Contains(src.NativeOperations, "background") {
		return &resourceError{422, "后台验证仅支持明确配置的原生 Responses API 卡", "background"}
	}
	if !src.Enabled || src.Deleted || !src.Configured || src.AuthStatus == "needs_reauth" || src.AuthStatus == "logged_out" || src.AuthStatus == "rejected" {
		return &resourceError{409, "先启用并配置此来源", "source"}
	}
	if model.ID == "" || !model.Enabled || model.SourceID != src.ID || model.UpstreamModel == "" || !slices.Contains(src.Models, model.UpstreamModel) {
		return &resourceError{409, "验证模型必须属于启用的直接来源", "model"}
	}
	if key.ID == "" || !keyValid(key, time.Now()) || key.SourceID != src.ID || key.RouteID != "" {
		return &resourceError{403, "请选择属于此来源且仍有效的现有直接 Key", "client_key_id"}
	}
	if !allowed(key.ProtocolAllowlist, "responses") || !allowed(key.OperationAllowlist, "generate") || !allowed(key.OperationAllowlist, "background") || !allowed(key.ModelAllowlist, model.UpstreamModel) {
		return &resourceError{403, "Key 需要此模型的 Responses、generate 与 background 权限", "client_key_id"}
	}
	return nil
}

type backgroundVerificationSink struct {
	header http.Header
	status int
	max    int64
	body   bytes.Buffer
}

func (w *backgroundVerificationSink) Header() http.Header { return w.header }
func (w *backgroundVerificationSink) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}
func (w *backgroundVerificationSink) Write(b []byte) (int, error) {
	if int64(w.body.Len()+len(b)) > w.max {
		return 0, errors.New("后台验证响应超过限制")
	}
	if w.status == 0 {
		w.status = 200
	}
	return w.body.Write(b)
}

// Root calls this from an explicit management verification operation with a
// user-selected existing Key. One ordinary resource admission owns creation
// and bounded GET polling; accounting, RPM/TPM, jobs, and slots stay shared.
func (a *App) verifyBackground(parent context.Context, src Source, model SourceModel, key ClientKey) (*Record, bool, error) {
	if err := validateBackgroundVerification(src, model, key); err != nil {
		return nil, false, err
	}
	a.mu.Lock()
	current, err := a.Store.source(src.ID)
	latestModel, me := a.Store.model(model.ID)
	var latestKey ClientKey
	var rawKey string
	ke := a.Store.DB.QueryRow("SELECT data FROM client_keys WHERE id=?", key.ID).Scan(&rawKey)
	if ke == nil {
		ke = json.Unmarshal([]byte(rawKey), &latestKey)
	}
	if err != nil || me != nil || ke != nil || current.Version != src.Version || current.Generation != src.Generation || current.AccountID != src.AccountID || current.AccountGeneration != src.AccountGeneration || latestModel.Version != model.Version || latestKey.Version != key.Version {
		a.mu.Unlock()
		return nil, false, &resourceError{409, "验证目标或 Key 已改变，请刷新后重试", "target"}
	}
	if err = validateBackgroundVerification(current, latestModel, latestKey); err != nil {
		a.mu.Unlock()
		return nil, false, err
	}
	price, err := a.Store.effectivePrice(latestModel.ID, latestModel.Price, time.Now())
	if err != nil {
		a.mu.Unlock()
		return nil, false, storageError()
	}
	if price != nil {
		current.Price = price
	}
	if a.stagedAdmission || a.stopping {
		a.mu.Unlock()
		return nil, false, &resourceError{503, "服务尚未开放验证准入", ""}
	}
	owner, err := a.beginBackupOwnerLocked()
	if err != nil {
		a.mu.Unlock()
		return nil, false, err
	}
	cfg := a.Config
	a.ownedTasks.Add(1)
	a.mu.Unlock()
	defer a.ownedTasks.Done()
	defer owner()
	ctx, cancel := context.WithTimeout(parent, time.Duration(cfg.TotalTimeout)*time.Second)
	defer cancel()
	ctx = context.WithValue(ctx, resourceVerificationContextKey{}, resourceVerificationContext{KeyID: key.ID, ModelID: model.ID, ModelVersion: model.Version, SourceID: src.ID})
	body := map[string]json.RawMessage{"model": json.RawMessage(encode(model.UpstreamModel)), "input": json.RawMessage(`"OK"`), "background": json.RawMessage("true"), "store": json.RawMessage("true"), "stream": json.RawMessage("false"), "max_output_tokens": json.RawMessage("32")}
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", "/v1/responses", bytes.NewReader(encoded))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	c, admitted, release, err := a.resourceAdmit(req, latestKey, current, "background")
	if err != nil {
		return nil, false, err
	}
	defer release()
	sink := &backgroundVerificationSink{header: http.Header{}, max: c.MaxResponse}
	a.backgroundResponsesAdmitted(sink, req, body, latestKey, current, model.UpstreamModel, model.UpstreamModel, c, admitted)
	requestID := sink.header.Get("X-Gateway-Request-Id")
	var record *Record
	readRecord := func() error {
		if requestID == "" {
			return nil
		}
		r, e := resourceReadRecord(a.Store.DB, requestID)
		if e != nil {
			return e
		}
		record = &r
		return nil
	}
	if err = readRecord(); err != nil {
		return nil, false, storageError()
	}
	if sink.status < 200 || sink.status >= 300 {
		return record, false, &resourceError{sink.status, "后台验证创建未完成；已有请求与任务保留，未重放", "background"}
	}
	if record == nil {
		return nil, false, errors.New("后台创建没有持久请求记录，不能证明资格")
	}
	var jobID string
	if err = a.Store.DB.QueryRow("SELECT id FROM jobs WHERE request_id=? AND key_id=? AND kind='background'", record.ID, key.ID).Scan(&jobID); err != nil {
		return record, false, storageError()
	}
	j, err := a.resourceReadJob(jobID, key.ID, "background")
	if err != nil {
		return record, false, err
	}
	if j.NativeID == "" {
		return record, false, errors.New("后台提交结果仍未知，保留待核对任务")
	}
	// A GET is required even for an immediately completed creation response:
	// storing and retrieving the same owned native response are separate proof.
	for {
		if admitted.Err() != nil {
			_ = readRecord()
			return record, false, admitted.Err()
		}
		if err = a.backgroundVerificationSnapshot(req, current, model, latestKey); err != nil {
			_ = readRecord()
			return record, false, err
		}
		response, e := a.resourceCall(admitted, current, "GET", "/responses/"+url.PathEscape(j.NativeID), nil, "")
		if e != nil {
			_ = readRecord()
			return record, false, e
		}
		wire, _, e := resourceJSON(response, c.MaxResponse)
		if e != nil {
			_ = readRecord()
			return record, false, e
		}
		native, e := resourceNativeID(wire["id"])
		if e != nil || native != j.NativeID {
			_ = readRecord()
			return record, false, &resourceError{409, "后台观测返回的 response ID 不属于原创建任务", "id"}
		}
		for tries := 0; tries < 3; tries++ {
			j, e = a.resourceReadJob(jobID, key.ID, "background")
			if e != nil {
				break
			}
			e = a.observeBackgroundJob(j, wire)
			var conflict *resourceError
			if !errors.As(e, &conflict) || conflict.Code != 409 {
				break
			}
		}
		if e != nil {
			_ = readRecord()
			return record, false, e
		}
		if e = readRecord(); e != nil {
			return record, false, storageError()
		}
		if e = a.backgroundVerificationSnapshot(req, current, model, latestKey); e != nil {
			return record, false, e
		}
		var rawState string
		_ = json.Unmarshal(wire["status"], &rawState)
		state, terminal := jobState("background", rawState)
		if terminal {
			if state != "completed" || record.Status != "succeeded" {
				return record, false, errors.New("后台任务终态未成功，原任务与账务保留")
			}
			b, _ := json.Marshal(wire)
			if !verificationText(b, "responses") {
				return record, false, errors.New("后台终态没有可核验的文本输出")
			}
			return record, true, nil
		}
		if state == "unrecognized" {
			return record, false, errors.New("后台状态未知，保留原任务继续只读观察")
		}
		j, e = a.resourceReadJob(jobID, key.ID, "background")
		if e != nil {
			return record, false, e
		}
		wait := 2 * time.Second
		if j.Next.Valid {
			if next, e := time.Parse(time.RFC3339Nano, j.Next.String); e == nil && time.Until(next) > 0 {
				wait = time.Until(next)
			}
		}
		timer := time.NewTimer(wait)
		select {
		case <-admitted.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			_ = readRecord()
			return record, false, admitted.Err()
		case <-timer.C:
		}
	}
}
func (a *App) backgroundVerificationSnapshot(r *http.Request, src Source, model SourceModel, key ClientKey) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	current, err := a.Store.source(src.ID)
	latest, ke := a.resourceCurrentKey(r)
	if err != nil || ke != nil || current.Version != src.Version || current.Generation != src.Generation || current.AccountID != src.AccountID || current.AccountGeneration != src.AccountGeneration || latest.Version != key.Version {
		return &resourceError{409, "后台观察期间来源、模型或 Key 已改变，不能绑定验证结果", "target"}
	}
	return validateBackgroundVerification(current, model, latest)
}
