package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type provider struct {
	srv   *httptest.Server
	key   *rsa.PrivateKey
	nonce string
	aud   string
	iss   string
}

func newProvider(t *testing.T) *provider {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	p := &provider{key: k, aud: "console"}
	mux := http.NewServeMux()
	p.srv = httptest.NewServer(mux)
	p.iss = p.srv.URL
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": p.srv.URL, "authorization_endpoint": p.srv.URL + "/auth",
			"token_endpoint": p.srv.URL + "/token", "jwks_uri": p.srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{"kty": "RSA", "kid": "k1", "use": "sig",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes())}}})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if u, pw, _ := r.BasicAuth(); u != "console" || pw != "s3cret" || r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
			return
		}
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{"iss": p.iss, "aud": p.aud, "sub": "u1", "preferred_username": "Alice",
			"groups": []string{"edge-admins"}, "nonce": p.nonce, "exp": time.Now().Add(time.Hour).Unix()})
		tok.Header["kid"] = "k1"
		s, _ := tok.SignedString(k)
		_ = json.NewEncoder(w).Encode(map[string]string{"id_token": s})
	})
	return p
}

func TestFlow(t *testing.T) {
	p := newProvider(t)
	defer p.srv.Close()
	c := &Client{}
	cfg := Config{Issuer: p.srv.URL, ClientID: "console", ClientSecret: "s3cret", RedirectURL: "https://console.example.com/api/v1/auth/oidc/callback"}
	authURL, f, err := c.Start(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(authURL)
	if u.Query().Get("code_challenge_method") != "S256" || u.Query().Get("state") != f.State || !strings.HasPrefix(authURL, p.srv.URL+"/auth?") {
		t.Fatalf("auth url %s", authURL)
	}
	p.nonce = f.Nonce
	claims, err := c.Finish(context.Background(), cfg, f, f.State, "good-code")
	if err != nil {
		t.Fatal(err)
	}
	if Strings(claims, "preferred_username")[0] != "Alice" || Strings(claims, "groups")[0] != "edge-admins" {
		t.Fatalf("claims %v", claims)
	}
	// state 不符、nonce 不符、受众不符、授权码无效都失败
	if _, err := c.Finish(context.Background(), cfg, f, "other", "good-code"); err == nil {
		t.Fatal("state mismatch accepted")
	}
	p.nonce = "replayed"
	if _, err := c.Finish(context.Background(), cfg, f, f.State, "good-code"); err == nil {
		t.Fatal("nonce mismatch accepted")
	}
	p.nonce, p.aud = f.Nonce, "someone-else"
	if _, err := c.Finish(context.Background(), cfg, f, f.State, "good-code"); err == nil {
		t.Fatal("wrong audience accepted")
	}
	p.aud = "console"
	if _, err := c.Finish(context.Background(), cfg, f, f.State, "bad-code"); err == nil {
		t.Fatal("bad code accepted")
	}
	// 伪造签名
	p.iss = "https://evil.example.com"
	if _, err := c.Finish(context.Background(), cfg, f, f.State, "good-code"); err == nil {
		t.Fatal("wrong issuer accepted")
	}
}
