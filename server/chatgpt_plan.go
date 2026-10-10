package server

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const (
	chatGPTPlanAPIKey = "gpt"
	chatGPTResource   = "https://api.openai.com/v1"
	authorizeEndpoint = "https://auth.openai.com/api/accounts/authorize"
	tokenEndpoint     = "https://auth.openai.com/api/accounts/oauth/token"
	keysEndpoint      = "https://auth.openai.com/.well-known/jwks.json"
)

var chatGPTHTTPClient = &http.Client{Timeout: 20 * time.Second}

type chatGPTCredentials struct {
	ClientID     string    `json:"client_id"`
	Email        string    `json:"email"`
	Subject      string    `json:"subject"`
	IDToken      string    `json:"id_token"`
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	Scopes       []string  `json:"scopes"`
	ExpiresAt    time.Time `json:"expires_at"`
	SavedAt      time.Time `json:"saved_at"`
}

type chatGPTConnectAttempt struct {
	State, Nonce, Verifier, RedirectURI string
	ClientID, IDTokenHint, LoginHint    string
	ExpectedSubject                     string
	ReturnTo                            string
	CreatedAt                           time.Time
}

type chatGPTPlan struct {
	mu               sync.Mutex
	path             string
	hostPath         string
	port             string
	pending          map[string]chatGPTConnectAttempt
	completedReturns map[string]string
	creds            *chatGPTCredentials
}

func newChatGPTPlan(dataDir string) *chatGPTPlan {
	port := strings.TrimSpace(os.Getenv("ARTEX_PUBLIC_PORT"))
	if port == "" {
		port = strings.TrimSpace(os.Getenv("ARTEX_PORT"))
	}
	if _, err := strconv.Atoi(port); err != nil || port == "" {
		port = "8787"
	}
	p := &chatGPTPlan{
		path:     filepath.Join(dataDir, "chatgpt", "credentials.json"),
		hostPath: filepath.Join(dataDir, "chatgpt", "host-id"),
		port:     port, pending: map[string]chatGPTConnectAttempt{}, completedReturns: map[string]string{},
	}
	if b, err := os.ReadFile(p.path); err == nil {
		var c chatGPTCredentials
		if json.Unmarshal(b, &c) == nil && c.ClientID != "" && c.RefreshToken != "" {
			p.creds = &c
		}
	}
	return p
}

func (p *chatGPTPlan) redirectURI(port string) string {
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		port = p.port
	}
	return "http://127.0.0.1:" + port + "/api/llm/chatgpt/callback"
}

func randomURLToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func (p *chatGPTPlan) hostID() (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if b, err := os.ReadFile(p.hostPath); err == nil {
		return strings.TrimSpace(string(b)), nil
	}
	if err := os.MkdirAll(filepath.Dir(p.hostPath), 0700); err != nil {
		return "", err
	}
	id := "urn:uuid:" + uuid.NewString()
	if err := os.WriteFile(p.hostPath, []byte(id+"\n"), 0600); err != nil {
		return "", err
	}
	return id, nil
}

func (p *chatGPTPlan) begin(returnTo, callbackPort string) (string, error) {
	hostID, err := p.hostID()
	if err != nil {
		return "", err
	}
	state, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	nonce, err := randomURLToken(32)
	if err != nil {
		return "", err
	}
	verifier, err := randomURLToken(48)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(verifier))
	clientID, idHint, loginHint := "dynamic_agent_client", "", ""
	expectedSubject := ""
	p.mu.Lock()
	if p.creds != nil {
		clientID, idHint, loginHint = p.creds.ClientID, p.creds.IDToken, p.creds.Email
		expectedSubject = p.creds.Subject
	}
	p.mu.Unlock()
	redirectURI := p.redirectURI(callbackPort)
	attempt := chatGPTConnectAttempt{State: state, Nonce: nonce, Verifier: verifier,
		RedirectURI: redirectURI, ClientID: clientID, IDTokenHint: idHint, LoginHint: loginHint, CreatedAt: time.Now()}
	attempt.ExpectedSubject = expectedSubject
	attempt.ReturnTo = returnTo
	p.mu.Lock()
	p.pending[state] = attempt
	p.mu.Unlock()

	q := url.Values{
		"client_id": {clientID}, "response_type": {"code"}, "redirect_uri": {redirectURI},
		"scope":    {"openid profile email offline_access resource.invoke chatgpt.tokens.use.direct"},
		"resource": {chatGPTResource}, "state": {state}, "nonce": {nonce},
		"code_challenge_method": {"S256"}, "code_challenge": {base64.RawURLEncoding.EncodeToString(hash[:])},
		"ext_agent_host_id": {hostID},
	}
	if clientID == "dynamic_agent_client" {
		q.Set("agent_name_hint", "ARTEX")
	} else {
		if idHint != "" {
			q.Set("id_token_hint", idHint)
		}
		if loginHint != "" {
			q.Set("login_hint", loginHint)
		}
	}
	return authorizeEndpoint + "?" + q.Encode(), nil
}

func (p *chatGPTPlan) complete(ctx context.Context, q url.Values) error {
	state := q.Get("state")
	p.mu.Lock()
	attempt, ok := p.pending[state]
	delete(p.pending, state)
	for k, v := range p.pending {
		if time.Since(v.CreatedAt) > 10*time.Minute {
			delete(p.pending, k)
		}
	}
	p.mu.Unlock()
	if !ok || time.Since(attempt.CreatedAt) > 10*time.Minute {
		return errors.New("授权状态已失效，请返回 ARTEX 重新连接")
	}
	if q.Get("error") != "" {
		detail := q.Get("error_description")
		if detail == "" {
			detail = q.Get("error")
		}
		return fmt.Errorf("ChatGPT 授权未完成：%s", detail)
	}
	code, clientID := q.Get("code"), q.Get("client_id")
	if code == "" {
		return errors.New("授权回调缺少 code")
	}
	if attempt.ClientID != "dynamic_agent_client" {
		if clientID != "" && clientID != attempt.ClientID {
			return errors.New("授权返回的客户端与当前连接不匹配")
		}
		clientID = attempt.ClientID
	}
	if clientID == "" || clientID == "dynamic_agent_client" {
		return errors.New("ChatGPT 未返回已注册的 client ID")
	}

	form := url.Values{"grant_type": {"authorization_code"}, "client_id": {clientID},
		"code": {code}, "code_verifier": {attempt.Verifier}, "redirect_uri": {attempt.RedirectURI}, "resource": {chatGPTResource}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := chatGPTHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("兑换授权令牌失败：%w", err)
	}
	defer resp.Body.Close()
	var token struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		IDToken          string `json:"id_token"`
		Scope            string `json:"scope"`
		ExpiresIn        int    `json:"expires_in"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return fmt.Errorf("读取令牌响应失败：%w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("令牌交换失败：%s", token.ErrorDescription)
	}
	if token.AccessToken == "" || token.RefreshToken == "" || token.IDToken == "" {
		return errors.New("令牌响应不完整")
	}
	scopes := strings.Fields(token.Scope)
	if !containsString(scopes, "chatgpt.tokens.use.direct") {
		return errors.New("此授权没有包含 ChatGPT 套餐调用权限，请重新连接并同意该权限")
	}
	claims, err := verifyChatGPTIDToken(ctx, token.IDToken, clientID, attempt.Nonce)
	if err != nil {
		return fmt.Errorf("验证 ChatGPT 身份失败：%w", err)
	}
	if attempt.ExpectedSubject != "" && claims.Subject != attempt.ExpectedSubject {
		return errors.New("登录的 ChatGPT 账号与已连接账号不同")
	}
	c := chatGPTCredentials{ClientID: clientID, Email: claims.Email, Subject: claims.Subject,
		IDToken: token.IDToken, AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		Scopes: scopes, ExpiresAt: time.Now().Add(time.Duration(token.ExpiresIn) * time.Second), SavedAt: time.Now().UTC()}
	if err := p.save(c); err != nil {
		return err
	}
	p.mu.Lock()
	p.creds = &c
	p.completedReturns[attempt.State] = attempt.ReturnTo
	p.mu.Unlock()
	return nil
}

func (p *chatGPTPlan) takeReturnTo(state string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	returnTo := p.completedReturns[state]
	delete(p.completedReturns, state)
	return returnTo
}

func containsString(haystack []string, want string) bool {
	for _, item := range haystack {
		if item == want {
			return true
		}
	}
	return false
}

type chatGPTIDClaims struct {
	Email string `json:"email"`
	Nonce string `json:"nonce"`
	jwt.RegisteredClaims
}

func verifyChatGPTIDToken(ctx context.Context, raw, clientID, nonce string) (*chatGPTIDClaims, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, keysEndpoint, nil)
	resp, err := chatGPTHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("读取身份验证密钥失败：HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct{ Kty, Kid, Alg, N, E string } `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	claims := &chatGPTIDClaims{}
	tok, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, fmt.Errorf("unsupported ID token algorithm")
		}
		kid, _ := token.Header["kid"].(string)
		for _, key := range set.Keys {
			if key.Kty != "RSA" || key.Kid != kid || (key.Alg != "" && key.Alg != "RS256") {
				continue
			}
			nBytes, nErr := base64.RawURLEncoding.DecodeString(key.N)
			eBytes, eErr := base64.RawURLEncoding.DecodeString(key.E)
			if nErr != nil || eErr != nil || len(nBytes) == 0 || len(eBytes) == 0 {
				return nil, errors.New("invalid JWKS key")
			}
			e := 0
			for _, b := range eBytes {
				e = e<<8 | int(b)
			}
			return &rsa.PublicKey{N: new(big.Int).SetBytes(nBytes), E: e}, nil
		}
		return nil, errors.New("ID token signing key not found")
	}, jwt.WithIssuer("https://auth.openai.com"), jwt.WithAudience(clientID), jwt.WithValidMethods([]string{"RS256"}))
	if err != nil {
		return nil, err
	}
	if !tok.Valid || claims.Nonce != nonce || claims.Subject == "" {
		return nil, errors.New("ID token claims did not match this sign-in")
	}
	return claims, nil
}

func (p *chatGPTPlan) save(c chatGPTCredentials) error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(p.path), ".credentials-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(b, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tmp, p.path); err != nil {
		return err
	}
	return os.Chmod(p.path, 0600)
}

func (p *chatGPTPlan) status() map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.creds == nil {
		return map[string]any{"connected": false, "plan_usage_enabled": false}
	}
	return map[string]any{"connected": true, "plan_usage_enabled": containsString(p.creds.Scopes, "chatgpt.tokens.use.direct"),
		"email": p.creds.Email, "expires_at": p.creds.ExpiresAt, "scopes": p.creds.Scopes}
}

func (p *chatGPTPlan) accessToken(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.creds == nil {
		return "", errors.New("请先连接 ChatGPT")
	}
	if time.Until(p.creds.ExpiresAt) > 2*time.Minute {
		return p.creds.AccessToken, nil
	}
	form := url.Values{"grant_type": {"refresh_token"}, "client_id": {p.creds.ClientID},
		"refresh_token": {p.creds.RefreshToken}, "resource": {chatGPTResource}}
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, tokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := chatGPTHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("刷新 ChatGPT 令牌失败：%w", err)
	}
	defer resp.Body.Close()
	var next struct {
		AccessToken      string `json:"access_token"`
		RefreshToken     string `json:"refresh_token"`
		ExpiresIn        int    `json:"expires_in"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&next); err != nil {
		return "", err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("刷新 ChatGPT 令牌失败：%s", next.ErrorDescription)
	}
	if next.AccessToken == "" || next.RefreshToken == "" {
		return "", errors.New("刷新令牌响应不完整")
	}
	c := *p.creds
	c.AccessToken, c.RefreshToken = next.AccessToken, next.RefreshToken
	c.ExpiresAt, c.SavedAt = time.Now().Add(time.Duration(next.ExpiresIn)*time.Second), time.Now().UTC()
	if err := p.save(c); err != nil {
		return "", err
	}
	p.creds = &c
	return c.AccessToken, nil
}

type chatGPTModel struct {
	Slug        string `json:"slug"`
	DisplayName string `json:"display_name"`
	Visibility  string `json:"visibility"`
}

func (p *chatGPTPlan) models(ctx context.Context) ([]chatGPTModel, error) {
	token, err := p.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, chatGPTResource+"/models", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := chatGPTHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("读取 ChatGPT 可用模型失败：HTTP %d", resp.StatusCode)
	}
	var result struct {
		Models []chatGPTModel `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	out := make([]chatGPTModel, 0, len(result.Models))
	for _, model := range result.Models {
		if model.Visibility == "list" && model.Slug != "" {
			out = append(out, model)
		}
	}
	return out, nil
}
