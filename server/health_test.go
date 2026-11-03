package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPublicHealthHasNoApplicationFingerprint(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "basic auth disabled"
		if enabled {
			name = "basic auth enabled"
		}
		t.Run(name, func(t *testing.T) {
			s, _ := basicAuthTestServer(t)
			if enabled {
				w := saveBasicAuthForTest(s, `{"enabled":true,"username":"entry","password":"test-pass"}`)
				if w.Code != http.StatusOK {
					t.Fatalf("enable gate: %d %s", w.Code, w.Body.String())
				}
			}
			h := s.Handler()
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				t.Run(method, func(t *testing.T) {
					w := httptest.NewRecorder()
					h.ServeHTTP(w, httptest.NewRequest(method, "/api/health", nil))
					if w.Code != http.StatusOK || w.Header().Get("WWW-Authenticate") != "" {
						t.Fatalf("public health unavailable: %d %v", w.Code, w.Header())
					}
					if w.Header().Get("Cache-Control") != "no-store" {
						t.Fatal("health response must not be cached")
					}
					var body map[string]any
					if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || len(body) != 1 || body["ok"] != true {
						t.Fatalf("health must only expose readiness: %s (err=%v)", w.Body.String(), err)
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
