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

// Markdown renders the report.
func Markdown(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 渗透测试报告 — %s\n\n", nz(in.Title, "未命名任务"))
	fmt.Fprintf(&b, "- **任务目标**：%s\n", nz(in.Goal, "（未指定）"))
	fmt.Fprintf(&b, "- **生成时间**：%s\n\n", in.GeneratedAt.Format("2006-01-02 15:04:05"))

	// summary
	fmt.Fprintf(&b, "## 摘要\n\n")
	fmt.Fprintf(&b, "- 确认发现：**%d** 个\n", len(in.Findings))
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
			if f.State == "verified" {
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
		render("已验证（复现器重放通过）", "这些结论有可重放的证据流量，复现器比对通过，可以直接交付。", verified, 1)
		render("待验证（仅有自述证据）", "这些结论还没有通过复现。交付前必须人工复核或重新触发复现；报告里不要把它们当成已确认漏洞。", pending, len(verified)+1)
	}

	return b.String()
}

func nz(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
