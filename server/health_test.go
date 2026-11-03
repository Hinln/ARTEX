package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthGateAndResponse(t *testing.T) {
	for _, tc := range []struct {
		name string
		gate bool
		auth string
		want int
	}{
		{name: "gate disabled", want: http.StatusOK},
		{name: "anonymous", gate: true, want: http.StatusUnauthorized},
		{name: "wrong credentials", gate: true, auth: "wrong", want: http.StatusUnauthorized},
		{name: "valid credentials", gate: true, auth: "basic", want: http.StatusOK},
		{name: "valid gate cookie", gate: true, auth: "cookie", want: http.StatusOK},
		{name: "admin JWT only", gate: true, auth: "jwt", want: http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := basicAuthTestServer(t)
			var cookie *http.Cookie
			if tc.gate {
				w := saveBasicAuthForTest(s, `{"enabled":true,"username":"entry","password":"test-pass"}`)
				if w.Code != http.StatusOK {
					t.Fatalf("enable gate: %d %s", w.Code, w.Body.String())
				}
				cookie = w.Result().Cookies()[0]
			}
			h := s.Handler()
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				t.Run(method, func(t *testing.T) {
					r := httptest.NewRequest(method, "/api/health", nil)
					switch tc.auth {
					case "wrong":
						r.SetBasicAuth("entry", "wrong-password")
					case "basic":
						r.SetBasicAuth("entry", "test-pass")
					case "cookie":
						r.AddCookie(cookie)
					case "jwt":
						token, err := signJWT(s.jwtKey)
						if err != nil {
							t.Fatal(err)
						}
						r.Header.Set("Authorization", "Bearer "+token)
					}
					w := httptest.NewRecorder()
					h.ServeHTTP(w, r)
					if w.Code != tc.want {
						t.Fatalf("status=%d want=%d: %s", w.Code, tc.want, w.Body.String())
					}
					if w.Header().Get("Cache-Control") != "no-store" {
						t.Fatal("health response must not be cached")
					}
					var body map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
						t.Fatal(err)
					}
					if tc.want == http.StatusUnauthorized {
						if w.Header().Get("WWW-Authenticate") != `Basic realm="Restricted", charset="UTF-8"` {
							t.Fatal("health must require Basic Auth")
						}
						for _, field := range []string{"ok", "service", "version"} {
							if _, exists := body[field]; exists {
								t.Fatalf("unauthenticated health exposed %s: %s", field, w.Body.String())
							}
						}
						return
					}
					if w.Header().Get("WWW-Authenticate") != "" || len(body) != 1 || body["ok"] != true {
						t.Fatalf("unexpected health response: %v %s", w.Header(), w.Body.String())
					}
				})
			}
		})
	}
}

func TestVersionRequiresAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		gate   bool
		basic  bool
		token  string
		cookie bool
		want   int
	}{
		{name: "anonymous", want: http.StatusUnauthorized},
		{name: "invalid JWT", token: "invalid", want: http.StatusUnauthorized},
		{name: "valid JWT", token: "valid", want: http.StatusOK},
		{name: "valid JWT cookie", token: "valid", cookie: true, want: http.StatusOK},
		{name: "gate anonymous", gate: true, want: http.StatusUnauthorized},
		{name: "gate Basic Auth only", gate: true, basic: true, want: http.StatusUnauthorized},
		{name: "gate JWT only", gate: true, token: "valid", want: http.StatusUnauthorized},
		{name: "gate invalid JWT", gate: true, basic: true, token: "invalid", want: http.StatusUnauthorized},
		{name: "gate both credentials", gate: true, basic: true, token: "valid", cookie: true, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _ := basicAuthTestServer(t)
			if tc.gate {
				w := saveBasicAuthForTest(s, `{"enabled":true,"username":"entry","password":"test-pass"}`)
				if w.Code != http.StatusOK {
					t.Fatalf("enable gate: %d %s", w.Code, w.Body.String())
				}
			}
			r := httptest.NewRequest(http.MethodGet, "/api/version", nil)
			if tc.basic {
				r.SetBasicAuth("entry", "test-pass")
			}
			token := tc.token
			if token == "valid" {
				var err error
				token, err = signJWT(s.jwtKey)
				if err != nil {
					t.Fatal(err)
				}
			}
			if token != "" {
				if tc.cookie {
					r.AddCookie(&http.Cookie{Name: "artex_token", Value: token})
				} else {
					r.Header.Set("Authorization", "Bearer "+token)
				}
			}
			w := httptest.NewRecorder()
			s.Handler().ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", w.Code, tc.want, w.Body.String())
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.want != http.StatusOK {
				if _, ok := body["version"]; ok {
					t.Fatalf("unauthorized request exposed version: %s", w.Body.String())
				}
				return
			}
			if len(body) != 1 || body["version"] != BuildVersion {
				t.Fatalf("unexpected authenticated version: %s", w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("version response must not be cached")
			}
		})
	}
}
