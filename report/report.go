// Package report renders a deliverable Markdown report from the exploration
// graph: confirmed findings (with severity/evidence) (docs §15 P5).
package report

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

// Input bundles what the report needs.
type Input struct {
	Title       string
	Goal        string
	GeneratedAt time.Time
	AssetCounts map[string]int
	Findings    []*db.Node
	// States 由调用方从 findings 表取：node_id -> verified/failed/pending。
	// 没有它，报告分不清「复现器重放通过」和「模型自己说能打通」。
	States map[int64]string
	// IncludePending 默认 false：报告只交已复现的结论。没跑通的东西不进正文，
	// 只留一行计数——「未证明/未找到」写进报告等于把活推给读者。
	IncludePending bool
}

// unprovenMarkers 是自述里「没搞清楚」的常见说法。这类句子出现在标题或摘要里，
// 说明这条根本没打完；即使状态被标成 verified 也不当成可交付结论。
var unprovenMarkers = []string{
	"未证明", "未找到", "无法确认", "未能验证", "无法验证", "未验证",
	"待确认", "尚不确定", "未能利用", "没跑通", "无法复现", "疑似", "可能存",
}

func (v findingView) unproven() bool {
	blob := v.Name + " " + v.VulnClass + " " + v.Summary
	for _, m := range unprovenMarkers {
		if strings.Contains(blob, m) {
			return true
		}
	}
	return false
}

type findingView struct {
	VulnClass string
	Name      string
	Severity  string
	Summary   string
	PoC       string
	State     string
}

func parseFinding(n *db.Node, states map[int64]string) findingView {
	var p struct {
		VulnClass string `json:"vulnclass"`
		Name      string `json:"name"`
		Severity  string `json:"severity"`
		Summary   string `json:"summary"`
		Evidence  struct {
			PoC string `json:"poc"`
		} `json:"evidence"`
	}
	_ = json.Unmarshal(n.Payload, &p)
	v := findingView{p.VulnClass, p.Name, p.Severity, p.Summary, p.Evidence.PoC, ""}
	if states != nil {
		v.State = states[n.ID]
	}
	return v
}

var sevRank = map[string]int{"critical": 0, "high": 1, "medium": 2, "low": 3, "": 4}

// countStates 给出「能进正文」与「进不去」的条数，口径与正文分组完全一致：
// 只有状态 verified 且文字里没有「没搞定」类措辞的，才算已复现结论。
func countStates(in Input) (verified, pending int) {
	for _, n := range in.Findings {
		v := parseFinding(n, in.States)
		if v.State == "verified" && !v.unproven() {
			verified++
			continue
		}
		pending++
	}
	return verified, pending
}

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 渗透测试报告 — %s\n\n", nz(in.Title, "未命名任务"))
	fmt.Fprintf(&b, "- **任务目标**：%s\n", nz(in.Goal, "（未指定）"))
	fmt.Fprintf(&b, "- **生成时间**：%s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	// summary
	fmt.Fprintf(&b, "## 摘要\n\n")
	verifiedCount, pendingCount := countStates(in)
	fmt.Fprintf(&b, "- 已复现结论：**%d** 个\n", verifiedCount)
	if pendingCount > 0 {
		fmt.Fprintf(&b, "- 未通过复现：**%d** 个（不入报告正文，见发现页「待验证」）\n", pendingCount)
	}
	fmt.Fprintf(&b, "- 资产：")
	var types []string
	for t := range in.AssetCounts {
		types = append(types, t)
	}
	sort.Strings(types)
	for i, t := range types {
		if i > 0 {
			b.WriteString("、")
		}
		fmt.Fprintf(&b, "%s %d", t, in.AssetCounts[t])
	}
	b.WriteString("\n\n")

	// findings
	if len(in.Findings) == 0 {
		b.WriteString("_本次未确认漏洞。_\n\n")
	} else {
		fs := make([]findingView, 0, len(in.Findings))
		for _, n := range in.Findings {
			fs = append(fs, parseFinding(n, in.States))
		}
		sort.SliceStable(fs, func(i, j int) bool { return sevRank[fs[i].Severity] < sevRank[fs[j].Severity] })
		var verified, pending []findingView
		for _, f := range fs {
			if f.State == "verified" && !f.unproven() {
				verified = append(verified, f)
			} else {
				pending = append(pending, f)
			}
		}
		render := func(title, note string, group []findingView, start int) {
			if len(group) == 0 {
				return
			}
			fmt.Fprintf(&b, "### %s（%d）\n\n", title, len(group))
			if note != "" {
				fmt.Fprintf(&b, "%s\n\n", note)
			}
			for i, f := range group {
				fmt.Fprintf(&b, "#### %d. [%s] %s\n\n", start+i, strings.ToUpper(nz(f.Severity, "info")), nz(f.Name, nz(f.VulnClass, "未分类")))
				fmt.Fprintf(&b, "%s\n\n", nz(f.Summary, ""))
				if f.PoC != "" {
					fmt.Fprintf(&b, "**PoC / 证据：**\n\n```\n%s\n```\n\n", f.PoC)
				}
			}
		}
		if in.IncludePending {
			render("已验证（复现器重放通过）", "这些结论有可重放的证据流量，复现器比对通过，可以直接交付。", verified, 1)
			render("待验证（仅有自述证据）", "这些结论还没有通过复现。交付前必须人工复核或重新触发复现；报告里不要把它们当成已确认漏洞。", pending, len(verified)+1)
		} else {
			render("已验证（复现器重放通过）", "这些结论有可重放的证据流量，复现器比对通过，可以直接交付。", verified, 1)
			if len(pending) > 0 {
				fmt.Fprintf(&b, "---\n\n本轮另有 **%d** 条结论没跑通复现，未纳入本报告。它们在发现页的「待验证」里，要么补证、要么按信息级记录，不要当成已确认漏洞交付。\n\n", len(pending))
			}
			if len(verified) == 0 {
				b.WriteString("本轮没有一条结论通过复现，因此报告不含漏洞正文。\n\n")
			}
		}
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
