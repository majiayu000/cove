package app

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

type sourceNetworkContext struct{}

func sourceNetwork(ctx context.Context, s Source) context.Context {
	return context.WithValue(ctx, sourceNetworkContext{}, s)
}
func validProxy(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" || !strings.Contains("|http|https|socks5|socks5h|", "|"+u.Scheme+"|") {
		return errors.New("代理必须是无凭据、无查询串的 http/https/socks5 地址")
	}
	return nil
}
func (a *App) doUpstream(req *http.Request, s Source) (*http.Response, error) {
	if inherited, ok := req.Context().Value(sourceNetworkContext{}).(Source); ok && s.ProxyURL == nil {
		s.ProxyURL = inherited.ProxyURL
	}
	if s.ProxyURL == nil {
		return a.HTTP.Do(req)
	}
	policy := *s.ProxyURL
	if err := validProxy(policy); err != nil {
		return nil, err
	}
	a.networkMu.Lock()
	client := a.networkClients[policy]
	if client == nil {
		transport, ok := a.HTTP.Transport.(*http.Transport)
		if !ok {
			a.networkMu.Unlock()
			return nil, errors.New("当前 transport 不支持来源代理配置")
		}
		tr := transport.Clone()
		tr.Proxy = nil
		if policy != "" {
			proxyURL, _ := url.Parse(policy)
			tr.Proxy = http.ProxyURL(proxyURL)
		}
		client = &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		if a.networkClients == nil {
			a.networkClients = map[string]*http.Client{}
		}
		a.networkClients[policy] = client
	}
	a.networkMu.Unlock()
	return client.Do(req)
}
func (a *App) CloseNetwork() {
	a.HTTP.CloseIdleConnections()
	a.networkMu.Lock()
	defer a.networkMu.Unlock()
	for _, c := range a.networkClients {
		c.CloseIdleConnections()
	}
}
