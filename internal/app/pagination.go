package app

import (
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type requestCursor struct {
	Filter   string
	Snapshot time.Time
	Started  string
	ID       string
	Expires  time.Time
}

func (a *App) recordsPage(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit := 50
	if v := q.Get("limit"); v != "" {
		var err error
		limit, err = strconv.Atoi(v)
		if err != nil || limit < 1 || limit > 200 {
			fail(w, 400, "limit 为 1 到 200", "limit")
			return
		}
	}
	for _, field := range []string{"from", "to"} {
		if value := q.Get(field); value != "" {
			t, e := time.Parse(time.RFC3339Nano, value)
			if e != nil {
				fail(w, 400, "时间筛选需要 RFC3339 时间", field)
				return
			}
			q.Set(field, t.UTC().Format(time.RFC3339Nano))
		}
	}
	if q.Get("from") != "" && q.Get("to") != "" {
		from, _ := time.Parse(time.RFC3339Nano, q.Get("from"))
		to, _ := time.Parse(time.RFC3339Nano, q.Get("to"))
		if !from.Before(to) {
			fail(w, 400, "from 必须早于 to", "from")
			return
		}
	}
	token := q.Get("cursor")
	q.Del("cursor")
	q.Del("limit")
	for _, field := range []string{"snapshot", "before_started", "before_id"} {
		if q.Has(field) {
			fail(w, 400, "不接受内部查询字段", field)
			return
		}
	}
	filter := q.Encode()
	now := time.Now().UTC()
	cursor := requestCursor{Filter: filter, Snapshot: now, Expires: now.Add(15 * time.Minute)}
	if token != "" {
		a.mu.Lock()
		saved, ok := a.pageCursors[token]
		a.mu.Unlock()
		if !ok || !saved.Expires.After(now) {
			fail(w, 410, "分页已过期或服务已重启，请从首页加载", "cursor")
			return
		}
		if saved.Filter != filter {
			fail(w, 400, "筛选条件已变化，请从首页加载", "cursor")
			return
		}
		cursor = saved
	}
	internal := url.Values{}
	for k, v := range q {
		internal[k] = v
	}
	internal.Set("snapshot", cursor.Snapshot.Format(time.RFC3339Nano))
	if cursor.Started != "" {
		internal.Set("before_started", cursor.Started)
		internal.Set("before_id", cursor.ID)
	}
	items, err := a.listRecords(internal, limit+1)
	if err != nil {
		fail(w, 503, storageError().Error(), "")
		return
	}
	var next any
	if len(items) > limit {
		items = items[:limit]
		last := items[len(items)-1]
		cursor.Started = last.Started.Format(time.RFC3339Nano)
		cursor.ID = last.ID
		token = id("page")
		a.mu.Lock()
		for key, value := range a.pageCursors {
			if !value.Expires.After(now) {
				delete(a.pageCursors, key)
			}
		}
		if len(a.pageCursors) >= 1024 {
			a.mu.Unlock()
			fail(w, 429, "分页查询过多，请稍后重新加载", "")
			return
		}
		a.pageCursors[token] = cursor
		a.mu.Unlock()
		next = token
	}
	writeJSON(w, 200, map[string]any{"items": items, "next_cursor": next, "snapshot_at": cursor.Snapshot})
}
