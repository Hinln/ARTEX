package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// authzText 取出工具返回的纯文本，测试里只关心判定文字。
func authzText(t *testing.T, ts *ToolSet, name string, args map[string]any) string {
	t.Helper()
	var tool = ts.authzDiff()
	if name == "oob_arm" {
		tool = ts.oobArm()
	} else if name == "oob_check" {
		tool = ts.oobCheck()
	}
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := tool.Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("%s call: %v", name, err)
	}
	var sb strings.Builder
	for _, b := range res.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

func TestAuthzIdenticalContentAcrossIdentities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/public":
			_, _ = w.Write([]byte("<html>login portal please sign in with your account</html>"))
		case "/owner-only":
			if r.Header.Get("X-User") == "alice" {
				_, _ = w.Write([]byte("order 1001 total 99.00 owner alice"))
				return
			}
			w.WriteHeader(403)
			_, _ = w.Write([]byte("无权限访问该订单"))
		}
	}))
	defer srv.Close()

	ts := &ToolSet{}
	out := authzText(t, ts, "authz_diff", map[string]any{
		"url": srv.URL + "/public",
		"identities": []map[string]any{
			{"name": "alice", "headers": map[string]string{"X-User": "alice"}},
			{"name": "bob", "headers": map[string]string{"X-User": "bob"}},
		},
	})
	if !strings.Contains(out, "完全一致的内容") {
		t.Fatalf("identical content not flagged:\n%s", out)
	}
	if !strings.Contains(out, "公共页面则忽略") {
		t.Fatalf("missing caveat for identical public page:\n%s", out)
	}
}

func TestAuthzOwnerDeniedOtherAllowed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-User") == "alice" {
			w.WriteHeader(403)
			_, _ = w.Write([]byte("无权限访问该订单"))
			return
		}
		_, _ = w.Write([]byte("order 1001 total 99.00 owner alice"))
	}))
	defer srv.Close()

	ts := &ToolSet{}
	out := authzText(t, ts, "authz_diff", map[string]any{
		"url":   srv.URL + "/order/1001",
		"owner": "alice",
		"identities": []map[string]any{
			{"name": "alice", "headers": map[string]string{"X-User": "alice"}},
			{"name": "bob", "headers": map[string]string{"X-User": "bob"}},
		},
	})
	if !strings.Contains(out, "越权重点怀疑对象") {
		t.Fatalf("bypass not flagged:\n%s", out)
	}
	if !strings.Contains(out, "被拒") {
		t.Fatalf("owner verdict missing:\n%s", out)
	}
}

func TestAuthzNeedsTwoIdentities(t *testing.T) {
	ts := &ToolSet{}
	out := authzText(t, ts, "authz_diff", map[string]any{
		"url":        "http://127.0.0.1:1/x",
		"identities": []map[string]any{{"name": "only"}},
	})
	if !strings.Contains(out, "至少给两个身份") {
		t.Fatalf("single identity accepted:\n%s", out)
	}
}

func TestAuthzSimilarityToleratesTimestamps(t *testing.T) {
	a := "order 1001 total 99.00 created 2026-01-01T00:00:00Z csrf abcdef"
	b := "order 1001 total 99.00 created 2026-01-01T00:00:09Z csrf zzzzzz"
	if !authzSimilar(a, b) || !authzLenClose(len(a), len(b)) {
		t.Fatalf("timestamps should not break similarity")
	}
	if authzSimilar("order 1001 total 99.00", "completely different payload here") {
		t.Fatalf("different bodies reported similar")
	}
}

func TestParseOOBRecords(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{`[]`, 0},
		{`[["10.0.0.9","2026-11-07 17:00:00","abcd.dnslog.cn"],["10.0.0.8","2026-11-07 17:00:01","efgh.dnslog.cn"]]`, 2},
		{`["10.0.0.9 abcd.dnslog.cn"]`, 1},
		{`<html>error</html>`, 0},
		{``, 0},
	}
	for _, c := range cases {
		if got := parseOOBRecords([]byte(c.in)); len(got) != c.want {
			t.Fatalf("parseOOBRecords(%q) = %v, want %d", c.in, got, c.want)
		}
	}
}

func TestOOBTokenIsUnique(t *testing.T) {
	seen := map[string]struct{}{}
	for i := 0; i < 200; i++ {
		tok := newOOBToken()
		if len(tok) != 8 {
			t.Fatalf("token length %d: %q", len(tok), tok)
		}
		if _, dup := seen[tok]; dup {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = struct{}{}
	}
}

func TestOOBRecordHost(t *testing.T) {
	if got := oobRecordHost(`10.0.0.9 2026-11-07 17:00:00 abcd.dnslog.cn`); got != "10.0.0.9" && !strings.Contains(got, ".") {
		t.Fatalf("unexpected host: %q", got)
	}
	if got := oobRecordHost(""); got != "" {
		t.Fatalf("empty record produced %q", got)
	}
}
