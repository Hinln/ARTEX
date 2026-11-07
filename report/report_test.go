package report

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Autumn-27/artex/db"
)

func nodeFor(t *testing.T, id int64, name string) *db.Node {
	t.Helper()
	payload, _ := json.Marshal(map[string]any{
		"vulnclass": "SQL注入", "name": name, "severity": "high",
		"summary": "登录接口 order 参数可注入", "evidence": map[string]any{"poc": "sqlmap -u ..."},
	})
	return &db.Node{ID: id, Kind: db.KindFinding, Payload: payload}
}

func TestMarkdownSeparatesVerifiedFromSelfReported(t *testing.T) {
	in := Input{
		Title: "t", Goal: "g", AssetCounts: map[string]int{"ip": 1},
		Findings: []*db.Node{nodeFor(t, 1, "登录注入"), nodeFor(t, 2, "后台越权")},
		States:   map[int64]string{1: "verified", 2: "pending"},
	}
	md := Markdown(in)
	v := strings.Index(md, "已验证")
	p := strings.Index(md, "待验证")
	if v < 0 || p < 0 {
		t.Fatalf("both sections must exist:\n%s", md)
	}
	if v > p {
		t.Fatal("verified section must come first")
	}
	if !strings.Contains(md[:p], "登录注入") {
		t.Fatal("verified finding must land in the verified section")
	}
	if !strings.Contains(md[p:], "后台越权") {
		t.Fatal("unverified finding must land in the pending section")
	}
	if strings.Contains(md[:p], "后台越权") {
		t.Fatal("unverified finding leaked into the verified section")
	}
}

func TestMarkdownWithoutStatesTreatsAllAsPending(t *testing.T) {
	md := Markdown(Input{Findings: []*db.Node{nodeFor(t, 9, "越权")}})
	if strings.Contains(md, "已验证（") {
		t.Fatalf("no state means nothing is verified:\n%s", md)
	}
	if !strings.Contains(md, "待验证") {
		t.Fatal("unverified finding must be listed under pending")
	}
}
