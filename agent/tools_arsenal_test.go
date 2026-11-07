package agent

import (
	"strings"
	"testing"
)

func TestParsePortSpecRejectsSweeps(t *testing.T) {
	if _, err := parsePortSpec("1-1024"); err == nil {
		t.Fatal("a 1024-port range must be refused, not silently truncated")
	}
	if _, err := parsePortSpec("1-65535"); err == nil {
		t.Fatal("full range must be refused")
	}
	ports, err := parsePortSpec("22,80,443")
	if err != nil {
		t.Fatalf("explicit list must pass: %v", err)
	}
	if len(ports) != 3 || ports[0] != 22 || ports[2] != 443 {
		t.Fatalf("unexpected ports: %v", ports)
	}
	got, err := parsePortSpec("8000-8003")
	if err != nil {
		t.Fatalf("small range must pass: %v", err)
	}
	if len(got) != 4 || got[0] != 8000 || got[3] != 8003 {
		t.Fatalf("unexpected expansion: %v", got)
	}
	if _, err := parsePortSpec("0"); err == nil {
		t.Fatal("port 0 is not a probe target")
	}
}

func TestSplitHTTPParsesStatusAndHeaders(t *testing.T) {
	raw := "HTTP/1.1 302 Found\r\nServer: nginx/1.24.0\r\nLocation: /login\r\nSet-Cookie: SESSION=abc; Path=/\r\n\r\n<html><head><title>Moved</title></head></html>"
	status, headers, body := splitHTTP(raw)
	if status != "HTTP/1.1 302 Found" {
		t.Fatalf("status = %q", status)
	}
	if headers["server"] != "nginx/1.24.0" || headers["location"] != "/login" {
		t.Fatalf("headers = %v", headers)
	}
	if !strings.Contains(body, "<title>Moved</title>") {
		t.Fatalf("body = %q", body)
	}
}

func TestParseResponseExtractsFormsAndParams(t *testing.T) {
	raw := "HTTP/1.1 200 OK\r\nServer: nginx\r\nContent-Type: text/html\r\n\r\n" +
		`<html><head><title>后台登录</title></head><body>` +
		`<form action="/admin/login" method="post"><input name="username"><input name="password"></form>` +
		`<a href="/api/v1/users?id=1">u</a></body></html>`
	status, headers, body := splitHTTP(raw)
	if status == "" || htmlTitle(body) != "后台登录" {
		t.Fatalf("title/status not parsed: %q %q", status, htmlTitle(body))
	}
	forms := htmlForms(body)
	if len(forms) != 1 || forms[0] != "POST /admin/login" {
		t.Fatalf("forms = %v", forms)
	}
	params := htmlParamNames(body)
	joined := strings.Join(params, ",")
	for _, want := range []string{"username", "password", "id"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("params missing %s: %v", want, params)
		}
	}
	hits := techHits(headers, body)
	if len(hits) == 0 || !strings.Contains(strings.Join(hits, ","), "nginx") {
		t.Fatalf("fingerprint missed nginx: %v", hits)
	}
}

func TestCollectEndpointsFindsAPIRoutesAndSkipsAnchors(t *testing.T) {
	text := `<script>fetch("/api/v1/orders/123");var u="https://cdn.example.com/x.js";` +
		`var p="/admin/internal/export";</script><a href="#top">x</a><a href="/help">h</a>`
	out := strings.Join(collectEndpoints(text, "https://app.example.com/base"), "\n")
	for _, want := range []string{"/api/v1/orders/123", "/admin/internal/export", "/help", "https://app.example.com/help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "#top") {
		t.Fatalf("anchors must be dropped:\n%s", out)
	}
}

func TestTechHitsRequireEvidence(t *testing.T) {
	if hits := techHits(map[string]string{}, "hello world"); len(hits) != 0 {
		t.Fatalf("no evidence must yield no hits, got %v", hits)
	}
	hits := techHits(map[string]string{"set-cookie": "JSESSIONID=1"}, "")
	if len(hits) != 1 || !strings.Contains(hits[0], "java") {
		t.Fatalf("cookie evidence missed: %v", hits)
	}
}

func TestResolveURL(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"https://a.example.com/x/y", "/api", "https://a.example.com/api"},
		{"https://a.example.com", "/api", "https://a.example.com/api"},
		{"https://a.example.com/base", "rel.html", "https://a.example.com/base/rel.html"},
		{"", "/api", "/api"},
		{"https://a.example.com", "https://b.example.com/z", "https://b.example.com/z"},
	}
	for _, c := range cases {
		if got := resolveURL(c.base, c.ref); got != c.want {
			t.Fatalf("resolveURL(%q,%q) = %q want %q", c.base, c.ref, got, c.want)
		}
	}
}
