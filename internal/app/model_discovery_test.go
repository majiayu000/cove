package app

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestSpecModelDiscoveryNativePaginationAndUnknown(t *testing.T) {
	for _, protocol := range []string{"gemini", "messages"} {
		t.Run(protocol, func(t *testing.T) {
			calls := 0
			a := contractApp(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if protocol == "gemini" {
					if r.Header.Get("X-Goog-Api-Key") != "synthetic" || r.Header.Get("Authorization") != "" {
						t.Error("wrong Gemini auth")
					}
					if calls == 1 {
						return contractResponse(`{"models":[{"name":"models/one","inputTokenLimit":100,"outputTokenLimit":20}],"nextPageToken":"next"}`, "application/json"), nil
					}
					if r.URL.Query().Get("pageToken") != "next" {
						t.Error("missing cursor")
					}
					return contractResponse(`{"models":[{"name":"models/two"}]}`, "application/json"), nil
				}
				if r.Header.Get("X-Api-Key") != "synthetic" || r.Header.Get("Anthropic-Version") == "" {
					t.Error("wrong Messages auth")
				}
				if calls == 1 {
					return contractResponse(`{"data":[{"id":"one","max_input_tokens":100,"max_tokens":20}],"has_more":true,"last_id":"one"}`, "application/json"), nil
				}
				if r.URL.Query().Get("after_id") != "one" {
					t.Error("missing cursor")
				}
				return contractResponse(`{"data":[{"id":"two"}],"has_more":false}`, "application/json"), nil
			})
			src, _ := a.Store.source("source")
			src.NativeProtocol = protocol
			items, e := a.discoverProviderModels(context.Background(), src, "synthetic", 8192)
			if e != nil || calls != 2 || len(items) != 2 || items[0].ContextLimit == nil || *items[0].ContextLimit != 100 || items[1].ContextLimit != nil || strings.Contains(items[1].ID, "models/") {
				t.Fatal(items, e, calls)
			}
		})
	}
}
func TestSpecModelDiscoveryRepeatedCursorRejectsPartial(t *testing.T) {
	a := contractApp(t, func(r *http.Request) (*http.Response, error) {
		return contractResponse(`{"models":[{"name":"models/one"}],"nextPageToken":"repeat"}`, "application/json"), nil
	})
	src, _ := a.Store.source("source")
	src.NativeProtocol = "gemini"
	items, e := a.discoverProviderModels(context.Background(), src, "synthetic", 8192)
	if e == nil || items != nil {
		t.Fatal("partial directory accepted", items, e)
	}
}
