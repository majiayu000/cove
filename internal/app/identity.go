package app

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strings"
	"time"
)

// Trust discovery only at the configured authorization origin. Claims are not
// used as identity until the signature, issuer, audience, expiry and nonce pass.
func (a *App) verifiedIdentity(ctx context.Context, token, nonce string) (string, string, error) {
	invalid := errors.New("授权身份验证失败，请重新登录")
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", "", invalid
	}
	var header struct {
		Alg   string `json:"alg"`
		KeyID string `json:"kid"`
	}
	h, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil || json.Unmarshal(h, &header) != nil || header.Alg != "RS256" || header.KeyID == "" {
		return "", "", invalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", "", invalid
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", "", invalid
	}
	var claims struct {
		Issuer          string          `json:"iss"`
		Audience        json.RawMessage `json:"aud"`
		AuthorizedParty string          `json:"azp"`
		Expires         int64           `json:"exp"`
		Issued          int64           `json:"iat"`
		NotBefore       int64           `json:"nbf"`
		Nonce           string          `json:"nonce"`
		Subject         string          `json:"sub"`
		Auth            struct {
			Account string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return "", "", invalid
	}
	now := time.Now().Unix()
	if claims.Subject == "" || claims.Auth.Account == "" || claims.Expires <= now || claims.Issued > now+30 || claims.NotBefore > now+30 || strings.TrimRight(claims.Issuer, "/") != strings.TrimRight(a.Config.Codex.AuthBaseURL, "/") {
		return "", "", invalid
	}
	var audiences []string
	var audience string
	if json.Unmarshal(claims.Audience, &audience) == nil {
		audiences = []string{audience}
	} else if json.Unmarshal(claims.Audience, &audiences) != nil {
		return "", "", invalid
	}
	matched := false
	for _, value := range audiences {
		if value == a.Config.Codex.ClientID {
			matched = true
		}
	}
	if !matched || len(audiences) > 1 && claims.AuthorizedParty != a.Config.Codex.ClientID || claims.AuthorizedParty != "" && claims.AuthorizedParty != a.Config.Codex.ClientID {
		return "", "", invalid
	}
	if nonce != "" && subtle.ConstantTimeCompare([]byte(nonce), []byte(claims.Nonce)) != 1 {
		return "", "", invalid
	}
	var metadata struct {
		Issuer  string `json:"issuer"`
		KeysURL string `json:"jwks_uri"`
	}
	if a.identityDocument(ctx, safeEndpoint(a.Config.Codex.AuthBaseURL, "/.well-known/openid-configuration"), &metadata) != nil || metadata.Issuer != claims.Issuer || origin(metadata.KeysURL) != origin(a.Config.Codex.AuthBaseURL) {
		return "", "", invalid
	}
	var keys struct {
		Keys []struct {
			ID   string `json:"kid"`
			Kind string `json:"kty"`
			Alg  string `json:"alg"`
			Use  string `json:"use"`
			N    string `json:"n"`
			E    string `json:"e"`
		} `json:"keys"`
	}
	if a.identityDocument(ctx, metadata.KeysURL, &keys) != nil {
		return "", "", invalid
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	for _, key := range keys.Keys {
		if key.ID != header.KeyID || key.Kind != "RSA" || key.Alg != "" && key.Alg != "RS256" || key.Use != "" && key.Use != "sig" {
			continue
		}
		n, nerr := base64.RawURLEncoding.DecodeString(key.N)
		e, eerr := base64.RawURLEncoding.DecodeString(key.E)
		exponent, modulus := new(big.Int).SetBytes(e), new(big.Int).SetBytes(n)
		if nerr != nil || eerr != nil || !exponent.IsInt64() || exponent.Int64() < 3 || exponent.Int64() > 1<<31-1 || modulus.BitLen() < 2048 || modulus.BitLen() > 8192 {
			continue
		}
		pub := &rsa.PublicKey{N: modulus, E: int(exponent.Int64())}
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, digest[:], signature) == nil {
			return claims.Auth.Account, claims.Subject, nil
		}
	}
	return "", "", invalid
}

func (a *App) identityDocument(ctx context.Context, address string, out any) error {
	r, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return err
	}
	response, err := a.HTTP.Do(r)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("identity metadata unavailable")
	}
	b, err := readLimited(response.Body, 1<<20)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, out)
}
