// Package oidc 是控制台的 OpenID Connect 登录客户端（授权码 + PKCE，规格书 7.1 M11，授权功能 sso）。
// 只用标准库与已有的 golang-jwt：发现文档、JWKS（RSA / ECDSA）、ID Token 校验（签名、iss、aud、exp、nonce）。
package oidc

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Config 是 OIDC 客户端配置。
type Config struct {
	Issuer       string
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string
}

type discovery struct {
	Issuer   string `json:"issuer"`
	AuthURL  string `json:"authorization_endpoint"`
	TokenURL string `json:"token_endpoint"`
	JWKSURL  string `json:"jwks_uri"`
}

// Client 缓存发现文档与 JWKS。
type Client struct {
	HTTP *http.Client
	mu   sync.Mutex
	disc map[string]*discovery
	keys map[string]map[string]any // jwks url -> kid -> key
	at   map[string]time.Time
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (c *Client) getJSON(ctx context.Context, u string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("GET %s: HTTP %d", u, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(v)
}

func (c *Client) discover(ctx context.Context, issuer string) (*discovery, error) {
	c.mu.Lock()
	if c.disc == nil {
		c.disc, c.keys, c.at = map[string]*discovery{}, map[string]map[string]any{}, map[string]time.Time{}
	}
	if d := c.disc[issuer]; d != nil && time.Since(c.at[issuer]) < time.Hour {
		c.mu.Unlock()
		return d, nil
	}
	c.mu.Unlock()
	var d discovery
	if err := c.getJSON(ctx, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
		return nil, err
	}
	if strings.TrimRight(d.Issuer, "/") != strings.TrimRight(issuer, "/") || d.AuthURL == "" || d.TokenURL == "" || d.JWKSURL == "" {
		return nil, errors.New("discovery document does not match the issuer")
	}
	c.mu.Lock()
	c.disc[issuer], c.at[issuer] = &d, time.Now()
	c.mu.Unlock()
	return &d, nil
}

func b64(s string) []byte {
	b, _ := base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
	return b
}

func (c *Client) jwks(ctx context.Context, u string, refresh bool) (map[string]any, error) {
	c.mu.Lock()
	ks := c.keys[u]
	c.mu.Unlock()
	if ks != nil && !refresh {
		return ks, nil
	}
	var doc struct {
		Keys []struct {
			Kty, Kid, Use, Alg, N, E, Crv, X, Y string
		} `json:"keys"`
	}
	if err := c.getJSON(ctx, u, &doc); err != nil {
		return nil, err
	}
	ks = map[string]any{}
	for _, k := range doc.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, e := new(big.Int).SetBytes(b64(k.N)), new(big.Int).SetBytes(b64(k.E))
			if n.BitLen() >= 2048 && e.IsInt64() {
				ks[k.Kid] = &rsa.PublicKey{N: n, E: int(e.Int64())}
			}
		case "EC":
			var curve elliptic.Curve
			switch k.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			default:
				continue
			}
			ks[k.Kid] = &ecdsa.PublicKey{Curve: curve, X: new(big.Int).SetBytes(b64(k.X)), Y: new(big.Int).SetBytes(b64(k.Y))}
		}
	}
	c.mu.Lock()
	c.keys[u] = ks
	c.mu.Unlock()
	return ks, nil
}

// Flow 是一次登录要暂存的参数（放在短期签名 Cookie 中）。
type Flow struct {
	State, Nonce, Verifier string
}

func random() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// Start 返回授权地址与需要暂存的参数。
func (c *Client) Start(ctx context.Context, cfg Config) (string, Flow, error) {
	d, err := c.discover(ctx, cfg.Issuer)
	if err != nil {
		return "", Flow{}, err
	}
	f := Flow{State: random(), Nonce: random(), Verifier: random()}
	sum := sha256.Sum256([]byte(f.Verifier))
	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = []string{"openid", "profile", "email"}
	}
	q := url.Values{"response_type": {"code"}, "client_id": {cfg.ClientID}, "redirect_uri": {cfg.RedirectURL}, "scope": {strings.Join(scopes, " ")},
		"state": {f.State}, "nonce": {f.Nonce}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}}
	sep := "?"
	if strings.Contains(d.AuthURL, "?") {
		sep = "&"
	}
	return d.AuthURL + sep + q.Encode(), f, nil
}

// Finish 用授权码换取并校验 ID Token，返回其中的声明。
func (c *Client) Finish(ctx context.Context, cfg Config, f Flow, state, code string) (jwt.MapClaims, error) {
	if state == "" || state != f.State || code == "" {
		return nil, errors.New("state mismatch")
	}
	d, err := c.discover(ctx, cfg.Issuer)
	if err != nil {
		return nil, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {cfg.RedirectURL}, "code_verifier": {f.Verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(url.QueryEscape(cfg.ClientID), url.QueryEscape(cfg.ClientSecret))
	resp, err := c.http().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var tr struct {
		IDToken string `json:"id_token"`
		Error   string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tr); err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 || tr.IDToken == "" {
		return nil, fmt.Errorf("token endpoint: HTTP %d %s", resp.StatusCode, tr.Error)
	}
	keyfunc := func(refresh bool) jwt.Keyfunc {
		return func(t *jwt.Token) (any, error) {
			ks, err := c.jwks(ctx, d.JWKSURL, refresh)
			if err != nil {
				return nil, err
			}
			kid, _ := t.Header["kid"].(string)
			if k, ok := ks[kid]; ok {
				return k, nil
			}
			if kid == "" && len(ks) == 1 {
				for _, k := range ks {
					return k, nil
				}
			}
			return nil, errors.New("unknown signing key")
		}
	}
	opts := []jwt.ParserOption{jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384"}), jwt.WithIssuer(d.Issuer),
		jwt.WithAudience(cfg.ClientID), jwt.WithExpirationRequired(), jwt.WithLeeway(time.Minute)}
	claims := jwt.MapClaims{}
	if _, err := jwt.ParseWithClaims(tr.IDToken, claims, keyfunc(false), opts...); err != nil {
		claims = jwt.MapClaims{}
		if _, err2 := jwt.ParseWithClaims(tr.IDToken, claims, keyfunc(true), opts...); err2 != nil { // 密钥轮换后刷新 JWKS 再试一次
			return nil, err
		}
	}
	if n, _ := claims["nonce"].(string); n != f.Nonce {
		return nil, errors.New("nonce mismatch")
	}
	return claims, nil
}

// Strings 读取字符串或字符串数组声明。
func Strings(claims jwt.MapClaims, name string) []string {
	switch v := claims[name].(type) {
	case string:
		return []string{v}
	case []any:
		var out []string
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}
