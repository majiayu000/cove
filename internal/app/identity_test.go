package app

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

var testSigningKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return key
})

func signedTestClaims(claims map[string]any) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"test-key"}`))
	body := base64.RawURLEncoding.EncodeToString([]byte(encode(claims)))
	sum := sha256.Sum256([]byte(header + "." + body))
	sig, err := rsa.SignPKCS1v15(rand.Reader, testSigningKey(), crypto.SHA256, sum[:])
	if err != nil {
		panic(err)
	}
	return header + "." + body + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func syntheticIdentityResponse(r *http.Request) *http.Response {
	issuer := "http://" + r.Host
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		return contractResponse(encode(map[string]string{"issuer": issuer, "jwks_uri": issuer + "/test-jwks"}), "application/json")
	case "/test-jwks":
		key := testSigningKey()
		return contractResponse(encode(map[string]any{"keys": []map[string]string{{"kid": "test-key", "kty": "RSA", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes())}}}), "application/json")
	}
	return nil
}

func TestContractSignedIdentityValidation(t *testing.T) {
	a := contractApp(t, nil)
	claims := map[string]any{"iss": a.Config.Codex.AuthBaseURL, "aud": a.Config.Codex.ClientID, "exp": time.Now().Add(time.Hour).Unix(), "sub": "subject", "nonce": "login-nonce", "https://api.openai.com/auth": map[string]string{"chatgpt_account_id": "account"}}
	for _, tc := range []struct {
		name   string
		modify func(map[string]any)
	}{
		{"valid", func(c map[string]any) {}},
		{"issuer", func(c map[string]any) { c["iss"] = "https://other.invalid" }},
		{"audience", func(c map[string]any) { c["aud"] = "other-client" }},
		{"expired", func(c map[string]any) { c["exp"] = time.Now().Add(-time.Second).Unix() }},
		{"nonce", func(c map[string]any) { c["nonce"] = "other-login" }},
		{"multi-audience", func(c map[string]any) { c["aud"] = []string{a.Config.Codex.ClientID, "other-client"} }},
		{"missing-subject", func(c map[string]any) { delete(c, "sub") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := map[string]any{}
			for k, v := range claims {
				copy[k] = v
			}
			tc.modify(copy)
			account, subject, err := a.verifiedIdentity(context.Background(), signedTestClaims(copy), "login-nonce")
			if tc.name == "valid" {
				if err != nil || account != "account" || subject != "subject" {
					t.Fatal("signed identity rejected")
				}
			} else if err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
	parts := strings.Split(signedTestClaims(claims), ".")
	claims["sub"] = "attacker"
	parts[1] = base64.RawURLEncoding.EncodeToString([]byte(encode(claims)))
	if _, _, err := a.verifiedIdentity(context.Background(), strings.Join(parts, "."), "login-nonce"); err == nil {
		t.Fatal("forged signature accepted")
	}
}
