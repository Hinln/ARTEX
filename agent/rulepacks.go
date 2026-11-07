package agent

// RulePacks: built-in SRC grading + testing-boundary standards, distilled from
// AutoHunter's EduSRC / enterprise prompt sets (app/agents/prompts.py,
// edu_scope.py, write_proof.py). Two decisions shape the shape of this file:
//
//   - The reject list becomes HARD constraints, not prompt text. Each rejected
//     class is written into task_constraints as kind='deny', so it rides the
//     existing constraintBlock injection (already framed as "最高优先级，凌驾于
//     一切探索启发式") and survives prompt edits. A worker cannot talk itself out
//     of a deny row the way it can talk itself past a paragraph.
//   - Severity stays the four ARTEX levels (critical/high/medium/low) with the
//     AutoHunter criteria attached, because the rest of the schema (findings
//     table, UI filters) is already keyed on those four strings.
//
// Everything is plain data + small helpers: no DB, no LLM, so the packs can be
// unit-tested and the server can install them at task creation.

import (
	"strings"

	"github.com/Autumn-27/artex/db"
)

// SeverityLevel is one grading band: its ARTEX severity plus the criteria that
// justify putting a finding there.
type SeverityLevel struct {
	Level    string   // critical | high | medium | low
	Criteria []string // what qualifies, in the pack's own words
}

// RulePack is one complete standard for a kind of engagement.
type RulePack struct {
	Key    string // "edusrc" | "enterprise" | (custom)
	Name   string
	Blurb  string // one line shown in the task UI
	Scope  string // what counts as the sensitive-data bar for this pack
	Accept []string
	Reject []string // each entry becomes a deny constraint at install time
	Proof  string   // harmless-evidence contract for write/delete/update findings
	Grades []SeverityLevel
	// ReproduceMinSeverity is the lowest severity that must be re-verified by the
	// system before it may enter the human review queue.
	ReproduceMinSeverity string
}

// BuiltinRulePacks returns the shipped packs. Callers install one per task; the
// returned slices are read-only.
func BuiltinRulePacks() []RulePack {
	return []RulePack{eduSRCPack(), enterprisePack()}
}

// RulePackByKey resolves a built-in pack by key (case-insensitive). ok=false when
// the key names no built-in pack — the caller then has a custom pack with no
// built-in baseline, which is legal (a task may rely on its own constraints).
func RulePackByKey(key string) (RulePack, bool) {
	k := strings.ToLower(strings.TrimSpace(key))
	for _, p := range BuiltinRulePacks() {
		if p.Key == k {
			return p, true
		}
	}
	return RulePack{}, false
}

func eduSRCPack() RulePack {
	return RulePack{
		Key:   "edusrc",
		Name:  "EduSRC（教育行业）",
		Blurb: "按实际危害定级；敏感数据只认四类；轰炸/反射 XSS/无实证泄露不收。",
		Scope: "敏感数据只认四类：身份证照片、人脸/大头照、身份证号码、密码哈希或明文口令。" +
			"其它（姓名/手机号/邮箱/地址/订单/校区/统计/运行状态/公开列表）默认不算敏感数据。",
		Accept: []string{
			"未授权/越权：资源本应鉴权，且突破后拿到四类敏感数据、可用凭证/token/session/DB 密码、getshell，或有状态差异的敏感写操作。接口本身没鉴权不算成果。",
			"上传：能传 HTML/SVG 到目标站自身域名且访问时 Content-Type: text/html → 存储型 XSS（中危），无需再证明弹窗；能解析执行脚本 → getshell（高危）。传到 OSS/CDN 或强制下载不算。",
			"短信 OTP 明文回显，且能打通任意用户登录或改密 → 中危~高危。仅验证码答案回显不收。",
			"无回显 RCE：有稳定侧信道（sleep 线性、DNS/HTTP 带外、结果落地后回读）即成立，判高危并继续补链。",
			"服务器被攻陷：本站正文被替换为赌博/色情/彩票、出现 deface、webshell 可执行（须 raw_response 可见）→ 高危~严重。图床/CDN 外链不是被黑。",
			"后台管理员弱口令：等级取决于登录后实际能做什么，不是「弱口令=高危」。",
		},
		Reject: []string{
			"不提交反射型 XSS / Self-XSS",
			"不提交 phpinfo、内网 IP、版本号、无实际利用的源码或域名泄露",
			"不提交需要管理员后台才能触发的漏洞、需要中间人攻击的漏洞、DoS/压测、钓鱼",
			"不提交无敏感操作的 CSRF",
			"不提交用户名枚举",
			"禁止短信轰炸/邮箱轰炸/邮件轰炸：发送验证码或通知的接口无频率限制只能刷短信邮件，不收，且禁止为取证连发",
			"不提交图形验证码/算术验证码答案回显（短信 OTP 回显可打通登录改密的除外）",
			"不提交扫描器出结果但给不出利用方法与实证的项",
			"不提交仅发现 secret/API key 却未证明能调通接口、伪造签名或盗刷配额",
			"不提交本就是面向公众的公开接口/公开展示数据",
			"不提交 CAS logout 的 service/redirect 纯 Open Redirect（仅 302 外跳）",
			"不把图床/CDN/OSS 外链、第三方 JS/CSS/字体/统计当作被黑",
		},
		Proof: "写/删/改类必须无害取证：① 自建哨兵 SRC_TEST_<rand>，建→改→删只碰自己这条，并旁路 GET/列表回读 before→after；② 授权对照：未登录应 401/403，低权登录态应 200；③ 只能碰已有对象时幂等回写原值，看 affectedRows≥1 且值未变。只看写接口自身回包、200、data:0、affectedRows:0 一律不算；做不到就标注缺哪一步，禁止硬删真实数据凑证据。",
		Grades: []SeverityLevel{
			{"critical", []string{"RCE / 上传 webshell / 获取服务器权限", "重要系统大量敏感信息泄露（如教务系统 SQL 注入脱出学生身份证）"}},
			{"high", []string{"普通系统权限 / 普通 SQL 注入", "批量盗取用户数据", "绕过认证进后台", "服务未授权访问且拿到够格数据或凭证", "后台管理员弱口令（按登录后实际危害）"}},
			{"medium", []string{"条件注入 / 任意文件操作", "水平越权", "业务逻辑缺陷", "短信 OTP 回显并导致登录/改密"}},
			{"low", []string{"非核心数据泄露", "需用户交互才能触发的漏洞"}},
		},
		ReproduceMinSeverity: "high",
	}
}

func enterprisePack() RulePack {
	return RulePack{
		Key:   "enterprise",
		Name:  "企业 SRC",
		Blurb: "围绕客户/员工/订单等受限数据与关键业务写操作定级；理论风险与半成品不收。",
		Scope: "受限数据：客户、员工、订单、合同、发票、供应商、工单、审批、财务、内部配置；" +
			"以及可用账号 / Token / Session / JWT / API Key / 云凭证 / 数据库密码 / 密码哈希。",
		Accept: []string{
			"RCE/getshell/命令执行/任意文件读写/反序列化/SSTI；无回显但有稳定侧信道同样收。",
			"SSRF 打进内网未授权服务，或读取云元数据临时凭证（AK/SK）；XXE 读到敏感文件或带外回连。",
			"JWT alg:none / 弱密钥爆破 / kid 注入，成功伪造他人或管理员登录态并调通受限接口。",
			"SQL/NoSQL 注入能读写真实业务数据或核心库。",
			"未授权/越权读取受限数据；管理后台弱口令且登录后能访问受限数据、配置或敏感操作。",
			"关键业务写操作：改订单/退款/审批/权限/用户资料/绑定关系/资金状态（须有状态变化证据）。",
			"任意用户接管、密码重置、OAuth/SSO 绑定覆盖、短信 OTP 回显或绕过。",
			"服务器被攻陷（本站正文被替换、webshell 可执行）→ 高危~严重。",
		},
		Reject: []string{
			"不提交仅发现 key/secret 而未证明能调用接口、伪造签名、读取受限数据或造成损失",
			"不提交仅证明 CORS 配置宽松、未配合敏感接口窃取数据的项",
			"不提交只看到 Swagger/Actuator/Druid/Nacos 页面或接口文档、未拿到可用凭证或业务数据",
			"不提交弱口令只看到菜单、空后台、接口文档的情况",
			"不提交仅版本号、内网 IP、路径、phpinfo、公开公告等低价值信息泄露",
			"不提交响应为错误码、空响应或没有状态变化证据的改密/接管/支付篡改结论",
			"不提交只贴 update/save/delete 返回 200/success、没有无害证据链的越权写",
			"禁止破坏性写删改、改或重置密码、批量导出/拉全表、下单/退款/转账、发短信邮件、删除或覆盖文件配置",
			"禁止 DoS/压测、大字典爆破、全端口宽扫、sqlmap dump/os-shell/file-write/sql-shell",
			"越权/IDOR 只读少量样本并脱敏；SQL 用布尔/延时/读单条；上传只用无害探针，不留后门",
		},
		Proof: "写/删/改类必须无害取证：① 自建哨兵 SRC_TEST_<rand> 建→改→删并旁路回读；② 未登录 401/403 对照低权 200；③ 幂等回写原值且值未变。只看写接口自身回包、200、空响应不算；禁止为取证触碰真实业务数据。",
		Grades: []SeverityLevel{
			{"critical", []string{"服务器权限", "核心库注入", "云/CI-CD 接管", "任意文件读写拿密钥"}},
			{"high", []string{"管理员权限", "批量企业敏感数据", "任意用户接管", "关键业务写操作"}},
			{"medium", []string{"局部越权", "单用户敏感数据", "有限业务影响", "条件型注入"}},
			{"low", []string{"低敏信息", "影响有限或需要较强前置条件"}},
		},
		ReproduceMinSeverity: "high",
	}
}

// --- install / render helpers ---

// rulePackOrigin tags the constraint rows this feature owns. It lets the install
// step be idempotent (a re-run sees its own rows and stops) without adding a
// migration to `explorations` just to hold one string.
const rulePackOrigin = "rulepack"

// InstallRulePack is the exported entry the server calls after creating a task.
// key="" or an unknown key is a no-op (the task runs with built-in behaviour).
// Returns how many constraint rows were created.
func InstallRulePack(ts *db.ExplorationStore, key string) (int, error) {
	p, ok := RulePackByKey(key)
	if !ok {
		return 0, nil
	}
	return installRulePack(ts, p, rulePackOrigin)
}

// ActiveRulePackKey reports which pack a task runs under, or "" for none. The
// server uses it so the task API can display the pack.
func ActiveRulePackKey(ts *db.ExplorationStore) string {
	p, ok := activeRulePack(ts)
	if !ok {
		return ""
	}
	return p.Key
}

// RulePackKeys lists the selectable built-in pack keys, for the UI.
func RulePackKeys() []string {
	var out []string
	for _, p := range BuiltinRulePacks() {
		out = append(out, p.Key)
	}
	return out
}

// installRulePack writes a pack's hard boundaries into the task's constraints.
// Replacing a pack is a replace, not a merge: rows owned by this origin are
// cleared first, otherwise edusrc's deny list survived an enterprise install and
// the task kept enforcing a standard the operator had moved away from.
func installRulePack(ts *db.ExplorationStore, p RulePack, origin string) (int, error) {
	if ts == nil || p.Key == "" {
		return 0, nil
	}
	if origin == "" {
		origin = rulePackOrigin
	}
	existing, err := ts.ListConstraints()
	if err != nil {
		return 0, err
	}
	current := ""
	for _, c := range existing {
		if c.Origin == origin && c.Kind == "allow" {
			current = c.Text
			break
		}
	}
	// Same pack already installed: no-op keeps createTask idempotent.
	if current == p.Key {
		n := 0
		for _, c := range existing {
			if c.Origin == origin && c.Kind == "deny" {
				n++
			}
		}
		return n, nil
	}
	if _, err := ts.DeleteConstraintsByOrigin(origin); err != nil {
		return 0, err
	}
	n := 0
	// Marker row first: its presence is what activeRulePack reads back to know
	// which pack this task runs under.
	if _, err := ts.AddConstraint("allow", p.Key, origin); err != nil {
		return 0, err
	}
	for _, text := range p.Reject {
		if _, err := ts.AddConstraint("deny", text, origin); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// ReproduceMinSeverityFor reports the reproduce floor of the pack a task runs
// under, or "" when no pack is installed. The server feeds this to its
// reproducer so the floor is a property of the pack instead of a constant
// buried in reproduce.go.
func ReproduceMinSeverityFor(ts *db.ExplorationStore) string {
	p, ok := activeRulePack(ts)
	if !ok {
		return ""
	}
	return p.ReproduceMinSeverity
}

// activeRulePack resolves the pack a task is running under: the first constraint
// row recorded with origin=rulepack names the pack key in its text. ok=false when
// the task never installed one (built-in behaviour, no pack).
func activeRulePack(ts *db.ExplorationStore) (RulePack, bool) {
	if ts == nil {
		return RulePack{}, false
	}
	rows, err := ts.ListConstraints()
	if err != nil {
		return RulePack{}, false
	}
	for _, c := range rows {
		if c.Origin != rulePackOrigin {
			continue
		}
		key := strings.TrimSpace(c.Text)
		if p, ok := RulePackByKey(key); ok {
			return p, true
		}
	}
	return RulePack{}, false
}

// rulePackBlock renders the pack's grading + evidence contract for an agent's
// system prompt, or "" when the task has no pack. The reject list is deliberately
// NOT repeated here: it is already a hard deny row and rides constraintBlock,
// which is framed as overriding everything below it.
func rulePackBlock(ts *db.ExplorationStore) string {
	p, ok := activeRulePack(ts)
	if !ok {
		return ""
	}
	return rulePackPrompt(p)
}

// rulePackPrompt renders one pack. Compact by design — this is injected into
// every planner round and every worker run.
func rulePackPrompt(p RulePack) string {
	if p.Key == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n【本任务 SRC 规则集：")
	b.WriteString(p.Name)
	b.WriteString("】\n")
	b.WriteString(p.Scope)
	b.WriteByte('\n')
	b.WriteString("可收：\n")
	for _, a := range p.Accept {
		b.WriteString("- ")
		b.WriteString(a)
		b.WriteByte('\n')
	}
	b.WriteString("定级口径（severity 必填其一，按实际危害而非漏洞类型标签）：\n")
	for _, g := range p.Grades {
		b.WriteString("- ")
		b.WriteString(g.Level)
		b.WriteString("：")
		b.WriteString(strings.Join(g.Criteria, "；"))
		b.WriteByte('\n')
	}
	if p.Proof != "" {
		b.WriteString("取证要求：")
		b.WriteString(p.Proof)
		b.WriteByte('\n')
	}
	b.WriteString("（本规则集的不收清单已作为硬性禁止约束写入任务约束，见【操作约束】，不得绕过。）")
	return b.String()
}
