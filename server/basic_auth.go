package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"
)

const basicAuthKey = "auth.http_basic"
const basicAuthCookie = "artex_basic_auth"
const basicAuthTTL = 12 * time.Hour
const basicAuthSaveTimeout = 10 * time.Second

type basicAuthConfig struct {
	Enabled      bool   `json:"enabled"`
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
	Revision     string `json:"revision"`
}

type basicAuthSettingsStore interface {
	GetSetting(string) (string, bool, error)
	SetSettingContext(context.Context, string, string) error
}

// Keep one atomic configuration in the DB and publish it only after a successful
// save. Requests use the in-memory snapshot, avoiding a DB read for every asset.
type basicAuthGate struct {
	saveMu sync.Mutex // serializes persistence without blocking request snapshots
	mu     sync.RWMutex
	cfg    basicAuthConfig
	store  basicAuthSettingsStore
}

func (g *basicAuthGate) load(store basicAuthSettingsStore) error {
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	raw, ok, err := store.GetSetting(basicAuthKey)
	if err != nil {
		return err
	}
	var cfg basicAuthConfig
	if ok {
		if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
			return fmt.Errorf("invalid HTTP Basic Auth configuration")
		}
	}
	if cfg.Enabled {
		if !validBasicUsername(cfg.Username) || cfg.Revision == "" {
			return fmt.Errorf("incomplete HTTP Basic Auth configuration")
		}
		if _, err := bcrypt.Cost([]byte(cfg.PasswordHash)); err != nil {
			return fmt.Errorf("invalid HTTP Basic Auth password hash")
		}
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.cfg, g.store = cfg, store
	return nil
}

func (g *basicAuthGate) snapshot() basicAuthConfig {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.cfg
}

func validBasicUsername(username string) bool {
	return strings.TrimSpace(username) != "" && len(username) <= 128 &&
		!strings.Contains(username, ":") && strings.IndexFunc(username, unicode.IsControl) == -1
}

// A separate signing key prevents a gate cookie from being used as an admin JWT.
func (s *Server) basicAuthSigningKey() []byte {
	m := hmac.New(sha256.New, s.jwtKey)
	m.Write([]byte("artex/http-basic-auth/session/v1"))
	return m.Sum(nil)
}

func (s *Server) validBasicAuthCookie(r *http.Request, cfg basicAuthConfig) bool {
	cookie, err := r.Cookie(basicAuthCookie)
	if err != nil || cfg.Revision == "" {
		return false
	}
	claims := &jwt.RegisteredClaims{}
	_, err = jwt.ParseWithClaims(cookie.Value, claims, func(*jwt.Token) (any, error) {
		return s.basicAuthSigningKey(), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(),
		jwt.WithAudience("artex-basic-auth"), jwt.WithSubject(cfg.Revision))
	return err == nil
}

func (s *Server) setBasicAuthCookie(w http.ResponseWriter, r *http.Request, cfg basicAuthConfig) error {
	cookie := &http.Cookie{
		Name: basicAuthCookie, Path: "/", HttpOnly: true,
		Secure:   r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https"),
		SameSite: http.SameSiteStrictMode, MaxAge: -1,
	}
	if cfg.Enabled {
		now := time.Now()
		token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
			Subject: cfg.Revision, Audience: jwt.ClaimStrings{"artex-basic-auth"},
			IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(basicAuthTTL)),
		}).SignedString(s.basicAuthSigningKey())
		if err != nil {
			return err
		}
		cookie.Value, cookie.MaxAge = token, int(basicAuthTTL.Seconds())
	}
	http.SetCookie(w, cookie)
	return nil
}

func (s *Server) requireBasicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := s.basicAuth.snapshot()
		// Health probes stay public; CORS preflight contains no credentials and
		// the API CORS handler returns 204 without invoking any business handler.
		if !cfg.Enabled || (r.URL.Path == "/api/health" && (r.Method == "GET" || r.Method == "HEAD")) ||
			(r.Method == "OPTIONS" && strings.HasPrefix(r.URL.Path, "/api/")) {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if s.validBasicAuthCookie(r, cfg) {
			next.ServeHTTP(w, r)
			return
		}
		username, password, ok := r.BasicAuth()
		if !ok || len(password) > maxPasswordBytes || username != cfg.Username ||
			bcrypt.CompareHashAndPassword([]byte(cfg.PasswordHash), []byte(password)) != nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="Restricted", charset="UTF-8"`)
			writeErr(w, http.StatusUnauthorized, "请先完成 HTTP Basic Auth 验证")
			return
		}
		// Browser API requests already use Authorization: Bearer. A separate
		// HttpOnly cookie keeps the gate satisfied without replacing that header.
		if err := s.setBasicAuthCookie(w, r, cfg); err != nil {
			writeErr(w, 500, "认证会话生成失败")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func basicAuthPublicSettings(cfg basicAuthConfig) map[string]any {
	return map[string]any{"enabled": cfg.Enabled, "username": cfg.Username, "password_set": cfg.PasswordHash != ""}
}

func (s *Server) getBasicAuthSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, basicAuthPublicSettings(s.basicAuth.snapshot()))
}

func (s *Server) saveBasicAuthSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled  *bool   `json:"enabled"`
		Username *string `json:"username"`
		Password string  `json:"password"`
	}
	if err := decode(r, &req); err != nil {
		writeErr(w, 400, "请求格式错误")
		return
	}
	g := &s.basicAuth
	g.saveMu.Lock()
	defer g.saveMu.Unlock()
	g.mu.RLock()
	cfg, store := g.cfg, g.store
	g.mu.RUnlock()
	if store == nil {
		writeErr(w, 503, errDataSourceUnavailable)
		return
	}
	if req.Enabled != nil {
		cfg.Enabled = *req.Enabled
	}
	if req.Username != nil {
		cfg.Username = strings.TrimSpace(*req.Username)
	}
	if (cfg.Enabled || cfg.Username != "") && !validBasicUsername(cfg.Username) {
		writeErr(w, 400, "Basic Auth 用户名不能为空、超过 128 字节或包含冒号及控制字符")
		return
	}
	if req.Password != "" {
		if msg := validatePassword(req.Password); msg != "" {
			writeErr(w, 400, msg)
			return
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
		if err != nil {
			writeErr(w, 500, "密码加密失败")
			return
		}
		cfg.PasswordHash = string(hash)
	}
	if cfg.Enabled && cfg.PasswordHash == "" {
		writeErr(w, 400, "请先设置 Basic Auth 密码")
		return
	}
	// Every save revokes previous gate cookies, including disable/re-enable.
	cfg.Revision = uuid.NewString()
	data, err := json.Marshal(cfg)
	ctx, cancel := context.WithTimeout(r.Context(), basicAuthSaveTimeout)
	defer cancel()
	if err != nil || store.SetSettingContext(ctx, basicAuthKey, string(data)) != nil {
		writeErr(w, 503, "Basic Auth 配置保存失败，请重试")
		return
	}
	// Hashing, persistence and response output never hold the request mutex.
	// Publish only committed settings; requests keep using the previous snapshot
	// until this short critical section completes.
	g.mu.Lock()
	g.cfg = cfg
	g.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	// Keep the administrator who just saved the settings in their session.
	if err := s.setBasicAuthCookie(w, r, cfg); err != nil {
		writeErr(w, 500, "认证会话生成失败")
		return
	}
	writeJSON(w, 200, basicAuthPublicSettings(cfg))
}
