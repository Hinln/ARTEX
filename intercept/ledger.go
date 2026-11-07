package intercept

import (
	"strings"
	"sync"
	"time"
)

// 执行台账：每次工具调用的命令与输出都留一条，用来核对"证据是不是真的跑出来的"。
//
// 模型最常见的幻觉不是编漏洞，而是编输出——把"应该会返回什么"写成"实际返回了什么"。
// report_finding 要求 evidence 必须能在这本台账里找到对应片段，编的就对不上。

const (
	ledgerMaxEntries = 600
	ledgerMaxOutput  = 32 << 10
)

// Execution 是一次工具调用的可核对记录。
type Execution struct {
	TS      int64  `json:"ts"`
	Session string `json:"session"`
	Tool    string `json:"tool"`
	Command string `json:"command,omitempty"`
	Output  string `json:"output,omitempty"`
	IsError bool   `json:"is_error,omitempty"`
}

var execLedger = struct {
	sync.Mutex
	entries []Execution
}{entries: make([]Execution, 0, 128)}

// RecordExecution 由 guard 的 PostToolUse 调用，登记一次真实执行。
func RecordExecution(session, tool, command, output string, isError bool) {
	if len(output) > ledgerMaxOutput {
		output = output[:ledgerMaxOutput]
	}
	if len(command) > 4096 {
		command = command[:4096]
	}
	e := Execution{TS: time.Now().Unix(), Session: session, Tool: tool, Command: command, Output: output, IsError: isError}
	execLedger.Lock()
	execLedger.entries = append(execLedger.entries, e)
	if len(execLedger.entries) > ledgerMaxEntries {
		execLedger.entries = execLedger.entries[len(execLedger.entries)-ledgerMaxEntries:]
	}
	execLedger.Unlock()
}

// RecentExecutions 返回最近 n 条（n<=0 取全部），可限定会话。
func RecentExecutions(session string, n int) []Execution {
	execLedger.Lock()
	defer execLedger.Unlock()
	out := make([]Execution, 0, len(execLedger.entries))
	for _, e := range execLedger.entries {
		if session != "" && e.Session != session {
			continue
		}
		out = append(out, e)
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// EvidenceAppears 判断一段证据文本是否真的出现在某次执行的输出（或命令）里。
// 逐行比对，命中任意一行足够长（>=12 字符）的片段即算真实。
func EvidenceAppears(session, evidence string) (bool, string) {
	needle := evidenceFragments(evidence)
	if len(needle) == 0 {
		return false, ""
	}
	candidates := RecentExecutions(session, 400)
	if len(candidates) == 0 {
		candidates = RecentExecutions("", 600)
	}
	for _, e := range candidates {
		hay := normalizeForMatch(e.Output + "\n" + e.Command)
		if hay == "" {
			continue
		}
		for _, frag := range needle {
			if strings.Contains(hay, frag) {
				return true, e.Command
			}
		}
	}
	return false, ""
}

// evidenceFragments 把证据拆成可匹配的归一化片段：短行不要，纯符号行不要。
func evidenceFragments(evidence string) []string {
	out := make([]string, 0, 12)
	for _, line := range strings.Split(evidence, "\n") {
		n := normalizeForMatch(line)
		if len(n) < 12 {
			continue
		}
		out = append(out, n)
		if len(out) >= 12 {
			break
		}
	}
	return out
}

func normalizeForMatch(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case r == '\x1b':
			// ANSI 转义整段丢弃
			space = true
			continue
		case r == '\n' || r == '\r' || r == '\t' || r == ' ':
			if !space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		if r < 32 {
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return strings.ToLower(strings.TrimSpace(b.String()))
}
