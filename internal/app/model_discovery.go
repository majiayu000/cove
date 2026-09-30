package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type discoveredProviderModel struct {
	ID           string `json:"id"`
	DisplayName  string `json:"display_name"`
	ContextLimit *int64 `json:"context_limit"`
	MaxOutput    *int64 `json:"max_output"`
}

func (a *App) discoverProviderModels(ctx context.Context, src Source, secret string, max int64) ([]discoveredProviderModel, error) {
	models := []discoveredProviderModel{}
	token := ""
	seen := map[string]bool{}
	total := int64(0)
	for page := 0; page < 100; page++ {
		address := safeEndpoint(src.BaseURL, "/models")
		if src.NativeProtocol == "gemini" {
			address = safeEndpoint(strings.TrimSuffix(strings.TrimRight(src.BaseURL, "/"), "/v1beta"), "/v1beta/models")
			if token != "" {
				address += "?pageToken=" + url.QueryEscape(token)
			}
		} else if token != "" {
			address += "?after_id=" + url.QueryEscape(token)
		}
		req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
		if err != nil {
			return nil, err
		}
		switch {
		case src.Kind == "none":
		case src.Provider == "azure":
			req.Header.Set("Api-Key", secret)
		case src.NativeProtocol == "messages":
			req.Header.Set("X-Api-Key", secret)
			req.Header.Set("Anthropic-Version", "2023-06-01")
		case src.NativeProtocol == "gemini":
			req.Header.Set("X-Goog-Api-Key", secret)
		default:
			req.Header.Set("Authorization", "Bearer "+secret)
		}
		resp, err := a.doUpstream(req, src)
		if err != nil {
			return nil, errors.New("模型目录网络请求失败，原目录保留")
		}
		raw, readErr := readLimited(resp.Body, max)
		resp.Body.Close()
		total += int64(len(raw))
		if readErr != nil || resp.StatusCode != 200 || total > max {
			return nil, errors.New("模型目录拒绝、超限或不完整，原目录保留")
		}
		var result struct {
			Data []struct {
				ID          string `json:"id"`
				DisplayName string `json:"display_name"`
				Context     *int64 `json:"max_input_tokens"`
				Output      *int64 `json:"max_tokens"`
			} `json:"data"`
			Models []struct {
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
				Input       *int64 `json:"inputTokenLimit"`
				Output      *int64 `json:"outputTokenLimit"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
			HasMore       bool   `json:"has_more"`
			LastID        string `json:"last_id"`
		}
		if json.Unmarshal(raw, &result) != nil {
			return nil, errors.New("模型目录JSON无效，原目录保留")
		}
		next := ""
		if src.NativeProtocol == "gemini" {
			if result.Models == nil {
				return nil, errors.New("未返回原生 Gemini models 目录")
			}
			for _, m := range result.Models {
				models = append(models, discoveredProviderModel{ID: strings.TrimPrefix(m.Name, "models/"), DisplayName: m.DisplayName, ContextLimit: m.Input, MaxOutput: m.Output})
			}
			next = result.NextPageToken
		} else {
			if result.Data == nil {
				return nil, errors.New("未返回 data 模型目录")
			}
			for _, m := range result.Data {
				models = append(models, discoveredProviderModel{ID: m.ID, DisplayName: m.DisplayName, ContextLimit: m.Context, MaxOutput: m.Output})
			}
			if result.HasMore {
				next = result.LastID
				if next == "" {
					return nil, errors.New("目录分页缺少 last_id")
				}
			}
		}
		if next == "" {
			return models, nil
		}
		if seen[next] {
			return nil, errors.New("目录分页游标循环")
		}
		seen[next] = true
		token = next
	}
	return nil, errors.New("模型目录分页超过100页，原目录保留")
}
