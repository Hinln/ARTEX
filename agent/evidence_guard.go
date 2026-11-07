package agent

import (
	"strings"

	"github.com/Autumn-27/artex/intercept"
)

// 证据核验：判一条 finding 是不是"真跑出来的"。
//
// 三件事分开判，任何一条不过就不入库或降级：
//   1. 证据里不能有推测词（假设/预计/理论上/模拟/示例）——那就是模型自己编的预期。
//   2. 证据里的关键片段必须能在执行台账里找到——对不上说明输出是编的。
//   3. 等级不能超过证据能撑起来的上限——内网 IP 泄露撑不起 high。

var hingeWords = []string{
	"假设", "预计", "推测", "理论上", "模拟", "示例输出", "示例结果", "预期输出", "预期结果",
	"大概会", "应当返回", "应该会返回", "推测输出", "假设输出",
	"hypothetical", "expected output", "should return",
}

// VerifyEvidence 返回是否可入库与给模型看的说明。
// session 用来优先匹配本会话的历史执行；空则全局匹配。
func VerifyEvidence(session, evidence string) (bool, string) {
	ev := strings.TrimSpace(evidence)
	if ev == "" {
		return false, "evidence 为空"
	}
	low := strings.ToLower(ev)
	for _, w := range hingeWords {
		if strings.Contains(low, strings.ToLower(w)) {
			return false, "证据里出现「" + w + "」——这是推测，不是跑出来的输出。重新执行一遍，把真实回显原样贴进来；确实没跑通就先用 record_fact 标 inferred 记着，不要当漏洞报。"
		}
	}
	ok, cmd := intercept.EvidenceAppears(session, ev)
	if ok {
		note := ""
		if cmd != "" {
			note = "证据已对上执行记录：" + truncateOneLine(cmd, 120)
		}
		return true, note
	}
	return false, "证据在本次任务的执行记录里找不到对应片段。你贴的输出没有真的跑出来过——请先执行命令或请求，再从真实回显里复制；如果用的是 MCP/自定义工具，也要在本次任务里真的调用过。"
}

// truncateOneLine 收成一行并截断，用于日志与提示。
func truncateOneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// ---- 等级校准 ----

const (
	rankInfo     = 1
	rankLow      = 1
	rankMedium   = 2
	rankHigh     = 3
	rankCritical = 4
)

var severityNames = map[int]string{1: "low", 2: "medium", 3: "high", 4: "critical"}

var severityRanks = map[string]int{
	"info": rankInfo, "information": rankInfo, "信息": rankInfo,
	"low": rankLow, "低危": rankLow, "低": rankLow,
	"medium": rankMedium, "中危": rankMedium, "中": rankMedium, "moderate": rankMedium,
	"high": rankHigh, "高危": rankHigh, "高": rankHigh,
	"critical": rankCritical, "严重": rankCritical, "严重漏洞": rankCritical, "紧急": rankCritical, "致命": rankCritical,
}

// classInfoOnly 一眼就知道只能算信息级、不单独收录为漏洞的类别。
var classInfoOnly = []string{
	"内网ip", "内网IP", "ip泄露", "IP泄露", "端口泄露", "端口暴露", "版本号", "指纹",
	"安全头", "响应头", "证书", "域名泄露", "堆栈泄露", "目录列表", "robots",
	"server版本", "banner",
}

// classCeiling 按漏洞类别给出「这一类最多能评到几级」。
func classCeiling(class string) (int, string) {
	c := strings.ToLower(strings.TrimSpace(class))
	for _, k := range classInfoOnly {
		if strings.Contains(c, strings.ToLower(k)) {
			return rankInfo, "类别属于信息级（" + class + "），不单独收录为漏洞"
		}
	}
	switch {
	case strings.Contains(c, "rce"), strings.Contains(c, "命令执行"), strings.Contains(c, "代码执行"),
		strings.Contains(c, "反序列化"), strings.Contains(c, "文件上传"), strings.Contains(c, "webshell"):
		return rankCritical, "命令执行类：需要实际执行回显才能上 critical"
	case strings.Contains(c, "sql"), strings.Contains(c, "注入"):
		return rankHigh, "注入类：需要有可复核的注入回显/时延/数据行"
	case strings.Contains(c, "文件读取"), strings.Contains(c, "任意文件"), strings.Contains(c, "路径穿越"):
		return rankHigh, "任意文件读取：需要贴出读到的文件内容"
	case strings.Contains(c, "ssrf"):
		return rankHigh, "SSRF：需要带外命中或内网响应内容"
	case strings.Contains(c, "越权"), strings.Contains(c, "idor"), strings.Contains(c, "未授权"):
		return rankHigh, "越权类：需要两个身份的真实响应对比"
	case strings.Contains(c, "xss"), strings.Contains(c, "csrf"), strings.Contains(c, "点击劫持"):
		return rankMedium, "XSS/CSRF 类：无敏感操作时最高 medium"
	case strings.Contains(c, "弱口令"), strings.Contains(c, "登录"):
		return rankHigh, "登录类：需要登录成功后的后台内容"
	}
	return rankHigh, ""
}

// evidenceCeiling 看证据本身能撑起多高。
func evidenceCeiling(evidence string) (int, string) {
	e := strings.ToLower(evidence)
	switch {
	case strings.Contains(e, "uid=0(root)"), strings.Contains(e, "nt authority\\system"),
		strings.Contains(e, "nt authority\\system"), strings.Contains(e, "root:x:0:0"),
		strings.Contains(e, "whoami") && strings.Contains(e, "system"):
		return rankCritical, "证据里出现系统权限回显"
	case strings.Contains(e, "you have an error in your sql syntax"), strings.Contains(e, "sqlstate"),
		strings.Contains(e, "ora-"), strings.Contains(e, "pg::syntaxerror"), strings.Contains(e, "unclosed quotation"),
		strings.Contains(e, "dnslog.cn"), strings.Contains(e, "/etc/passwd") && strings.Contains(e, "/bin/bash"),
		strings.Contains(e, "root:x:0:0"):
		return rankHigh, "证据含报错指纹或带外命中"
	case strings.Contains(e, "http/1.1"), strings.Contains(e, "http/2"), strings.Contains(e, "content-type:"),
		strings.Contains(e, "set-cookie"):
		return rankMedium, "证据只有 HTTP 响应，没有进一步利用结果"
	}
	return rankMedium, "证据只到「有响应差异」，撑不起 high 以上"
}

// CalibrateSeverity 把模型自报的等级按类别与证据压到真实上限。
// 返回最终等级、是否被改、以及给模型看的原因。
func CalibrateSeverity(class, evidence, claimed string) (string, bool, string) {
	want, ok := severityRanks[strings.ToLower(strings.TrimSpace(claimed))]
	if !ok {
		want = rankMedium
	}
	clsCap, clsWhy := classCeiling(class)
	evCap, evWhy := evidenceCeiling(evidence)
	// Policy is the SRC's own grade table: a class the platform files as 中危
	// cannot be published as 严重 just because the model wrote 严重.
	polCap, polWhy := PolicyGrade(class)

	final := want
	reasons := make([]string, 0, 3)
	if polCap < final {
		final = polCap
		if polWhy != "" {
			reasons = append(reasons, polWhy)
		}
	}
	if clsCap < final {
		final = clsCap
		if clsWhy != "" {
			reasons = append(reasons, clsWhy)
		}
	}
	if evCap < final {
		final = evCap
		if evWhy != "" {
			reasons = append(reasons, evWhy)
		}
	}
	name := severityNames[final]
	if final == want && len(reasons) == 0 {
		return name, false, ""
	}
	note := ""
	if len(reasons) > 0 {
		note = strings.Join(reasons, "；")
	}
	if final != want {
		return name, true, "等级从 " + strings.ToLower(claimed) + " 校正为 " + name + "：" + note
	}
	return name, false, note
}

// InfoOnlyClass 判断是否属于「只算信息、不收录为漏洞」的类别。
func InfoOnlyClass(class string) bool {
	cap, _ := classCeiling(class)
	if cap > rankInfo {
		return false
	}
	c := strings.ToLower(strings.TrimSpace(class))
	for _, k := range classInfoOnly {
		if strings.Contains(c, strings.ToLower(k)) {
			return true
		}
	}
	return false
}
