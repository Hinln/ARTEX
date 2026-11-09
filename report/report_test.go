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

// 硬规则：默认只交已复现的结论。没跑通的不进正文，只留一行计数。
func TestMarkdownDropsUnverifiedByDefault(t *testing.T) {
	in := Input{
		Title: "t", Goal: "g", AssetCounts: map[string]int{"ip": 1},
		Findings: []*db.Node{nodeFor(t, 1, "登录注入"), nodeFor(t, 2, "后台越权")},
		States:   map[int64]string{1: "verified", 2: "pending"},
	}
	md := Markdown(in)
	if !strings.Contains(md, "登录注入") {
		t.Fatalf("verified finding must be in the report:\n%s", md)
	}
	if strings.Contains(md, "后台越权") {
		t.Fatalf("unverified finding must not be in the body:\n%s", md)
	}
	if !strings.Contains(md, "未通过复现：**1** 个") {
		t.Fatalf("pending count must be stated:\n%s", md)
	}
	if strings.Contains(md, "待验证（仅有自述证据）") {
		t.Fatalf("pending section must be opt-in:\n%s", md)
	}
}

func TestMarkdownListsPendingOnlyWhenAsked(t *testing.T) {
	in := Input{
		Findings:       []*db.Node{nodeFor(t, 1, "登录注入"), nodeFor(t, 2, "后台越权")},
		States:         map[int64]string{1: "verified", 2: "pending"},
		IncludePending: true,
	}
	md := Markdown(in)
	v := strings.Index(md, "### 已验证")
	p := strings.Index(md, "### 待验证")
	if v < 0 || p < 0 || v > p {
		t.Fatalf("opt-in report must keep both sections in order:\n%s", md)
	}
	if !strings.Contains(md[p:], "后台越权") {
		t.Fatalf("pending finding must land in the pending section:\n%s", md)
	}
}

func TestMarkdownWithoutStatesSaysNothingVerified(t *testing.T) {
	md := Markdown(Input{Findings: []*db.Node{nodeFor(t, 9, "越权")}})
	if strings.Contains(md, "越权") {
		t.Fatalf("no state means nothing may be claimed:\n%s", md)
	}
	if !strings.Contains(md, "没有一条结论通过复现") {
		t.Fatalf("report must say it has nothing verified:\n%s", md)
	}
}

// 自述里带「没搞定」措辞的，即使状态被标成 verified 也不当成可交付结论。
func TestMarkdownRejectsUnprovenWording(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"vulnclass": "弱口令", "name": "后台弱口令（未证明可登录）", "severity": "high",
		"summary": "未找到可用的口令组合", "evidence": map[string]any{"poc": ""},
	})
	n := &db.Node{ID: 3, Kind: db.KindFinding, Payload: payload}
	md := Markdown(Input{Findings: []*db.Node{n}, States: map[int64]string{3: "verified"}})
	if strings.Contains(md, "后台弱口令") {
		t.Fatalf("unproven wording must keep the finding out of the body:\n%s", md)
	}
	if !strings.Contains(md, "没有一条结论通过复现") {
		t.Fatalf("report must admit it has nothing:\n%s", md)
	}
}

func TestCountStatesMatchesBody(t *testing.T) {
	in := Input{
		Findings: []*db.Node{nodeFor(t, 1, "a"), nodeFor(t, 2, "b"), nodeFor(t, 3, "c")},
		States:   map[int64]string{1: "verified", 2: "failed", 3: "verified"},
	}
	if v, p := countStates(in); v != 2 || p != 1 {
		t.Fatalf("want 2 verified / 1 pending, got %d/%d", v, p)
	}
}
