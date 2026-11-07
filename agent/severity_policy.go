package agent

import "strings"

// 定级策略：先按 EduSRC 的公开口径落一套通用规则，企业 SRC 的差异走同一个入口换表。
//
// 顺序是：先拒收（收都不收的东西不该有等级），再按类别给基准等级，
// 最后和三重上限（类别 / 证据 / 声明）取最小值。模型填什么只是"上限"，
// 不是"结论"。

// severitiesNotAccepted 是 EduSRC 明确不收的类别。命中就不入库，直接给出替代动作。
var severitiesNotAccepted = []struct {
	name   string
	keys   []string
	reason string
}{
	{"反射型 XSS / Self-XSS", []string{"反射型xss", "反射xss", "reflected xss", "self-xss", "self xss"}, "EduSRC 不收反射型 XSS 与 Self-XSS；要交就打存储型，或把它链到账号接管"},
	{"无利用的信息泄露", []string{"phpinfo", "内网ip", "内网 ip", "版本号泄露", "指纹泄露", "安全头缺失", "目录列表", "banner"}, "没有实际利用的信息泄露不收录；用 record_fact 记线索，别当漏洞"},
	{"需登录后台才触发", []string{"后台登录后", "管理员登录后", "需登录后台"}, "需要登录管理员后台才能触发的漏洞不收录"},
	{"需中间人", []string{"中间人", "man-in-the-middle", "mitm"}, "需要中间人攻击才能成立的漏洞不收录"},
	{"无敏感操作的 CSRF", []string{"csrf", "跨站请求伪造"}, "没有敏感操作的 CSRF 不收录；要交得证明改密/改配置/资金操作实际发生"},
	{"拒绝服务", []string{"拒绝服务", "dos", "ddos", "资源耗尽", "崩溃型"}, "DoS 类不收录"},
	{"扫描器裸结果", []string{"扫描器", "nuclei", "模板命中", "扫描结果"}, "扫描器输出不算证据；要有手工请求与响应"},
	{"图形验证码回显", []string{"图形验证码", "算术验证码"}, "只有短信 OTP 明文回显才算漏洞，图形码回显不算"},
	{"纯开放重定向", []string{"open redirect", "开放重定向", "任意跳转"}, "纯跳转属于钓鱼风险，不收录；除非继续证明 ticket/session 泄露"},
	{"只发现密钥", []string{"发现密钥", "硬编码密钥", "硬编码密钥泄露", "apikey泄露", "api key 泄露", "secret 泄露", "secret泄露"}, "只发现 secret 不算漏洞；要用它伪造签名调通接口或拿到数据"},
	{"CORS 配置过松", []string{"cors 配置", "cors配置", "跨域配置过松", "跨域资源共享配置"}, "要找一个敏感接口实证跨域窃取到数据"},
	{"无验证码批量注册", []string{"批量注册", "无验证码注册"}, "单独价值低；要链到撞库/越权/薅羊毛才算"},
}

// eduGradeTable 按类别给基准等级：4=严重 3=高危 2=中危 1=低危。
var eduGradeTable = []struct {
	name string
	keys []string
	rank int
}{
	{"命令执行 / 代码执行", []string{"rce", "命令执行", "代码执行", "命令注入", "code execution", "反序列化", "反序列化漏洞"}, 4},
	{"上传 getshell", []string{"webshell", "getshell", "上传", "文件上传", "任意文件上传"}, 4},
	{"数据库整体落地", []string{"sql注入dump", "dump 学生", "整库", "拖库", "数据库权限"}, 4},
	{"认证绕过 / 用户接管", []string{"认证绕过", "登录绕过", "任意用户", "账号接管", "用户接管", "jwt 伪造", "sso bypass"}, 3},
	{"SQL 注入", []string{"sql注入", "sql 注入", "sql injection", "盲注"}, 3},
	{"未授权访问（有凭证/数据）", []string{"未授权访问", "未授权接口", "unauthorized"}, 3},
	{"垂直越权", []string{"垂直越权", "权限提升", "提权"}, 3},
	{"弱口令（有实际危害）", []string{"弱口令", "默认口令", "默认密码"}, 3},
	{"水平越权 / IDOR", []string{"水平越权", "越权", "idor", "对象引用"}, 2},
	{"文件读取 / 任意文件操作", []string{"文件读取", "任意文件读", "lfi", "路径穿越", "目录穿越"}, 2},
	{"SSRF", []string{"ssrf", "服务端请求伪造"}, 2},
	{"业务逻辑缺陷", []string{"业务逻辑", "逻辑缺陷", "并发", "重放", "支付逻辑"}, 2},
	{"存储型 XSS", []string{"存储型xss", "存储型 xss", "stored xss"}, 2},
	{"信息泄露（非四类敏感数据）", []string{"信息泄露", "源码泄露", "配置泄露", "日志泄露", "备份文件"}, 1},
}

// mustVerifyClasses 是"必须复现一次才允许当结论"的类别：这些都靠一次真实执行才成立，
// 说"能打通"但复现不出来，就不能进报告正文。
var mustVerifyKeys = []string{
	"rce", "命令执行", "代码执行", "命令注入", "反序列化", "webshell", "getshell", "上传",
	"sql注入", "sql 注入", "盲注", "越权", "idor", "认证绕过", "未授权", "ssrf", "文件读取", "lfi",
}

// MustVerify 判断该类别是否必须复现通过。
func MustVerify(vulnclass string) bool {
	low := strings.ToLower(vulnclass)
	for _, k := range mustVerifyKeys {
		if strings.Contains(low, k) {
			return true
		}
	}
	return false
}

// RejectReason 判断这个类别在 EduSRC 口径下是否根本不收；命中返回中文原因。
func RejectReason(vulnclass, summary string) (string, bool) {
	return RejectReasonFor("edu", vulnclass, summary)
}

// RejectReasonFor 按所选 SRC 口径判断是否根本不收。
func RejectReasonFor(policy, vulnclass, summary string) (string, bool) {
	table := severitiesNotAccepted
	if policy == "enterprise" {
		table = enterpriseNotAccepted
	}
	blob := strings.ToLower(vulnclass + " " + summary)
	for _, rule := range table {
		for _, k := range rule.keys {
			if strings.Contains(blob, k) {
				return rule.name + "：" + rule.reason, true
			}
		}
	}
	return "", false
}

// enterpriseNotAccepted 是 OPPO OSRC V5.0（2026-11-06 生效）明确列为「无危」或不收取的类别。
// 与 EduSRC 的差别要留意：反射型 XSS 在 OPPO 是低危而不是不收，所以不在这张表里。
var enterpriseNotAccepted = []struct {
	name   string
	keys   []string
	reason string
}{
	{"扫描器报告", []string{"扫描器", "自动化扫描", "nuclei", "模板命中", "扫描结果"}, "OPPO 明确不收自动化工具/扫描器报告；要交得有手工请求与响应"},
	{"内网 IP / 域名泄漏", []string{"内网ip", "内网 ip", "内网域名", "内部ip"}, "内网 IP、域名泄漏属于无危；只有继续利用成功才算"},
	{"版本号 / 中间件信息", []string{"版本号泄露", "中间件版本号", "软件版本", "banner 信息"}, "中间件与软件版本号单独不算漏洞"},
	{"Self-XSS", []string{"self-xss", "self xss"}, "Self-XSS 及只能靠 Self-XSS 利用的问题不收"},
	{"POST 型 XSS", []string{"post型xss", "post 型 xss"}, "POST 型 XSS 在 OPPO 属无危"},
	{"需钓鱼配合的 XSS", []string{"钓鱼xss", "需钓鱼", "社工xss"}, "需要结合钓鱼社工的 XSS 不收"},
	{"无敏感信息的 JSON Hijacking", []string{"json hijacking", "json劫持"}, "无敏感信息的 JSON Hijacking 不收"},
	{"无意义异常信息泄漏", []string{"异常信息泄露", "报错信息泄露", "堆栈泄漏"}, "无意义的异常信息泄漏属无危"},
	{"OPTIONS / TRACE 开启", []string{"options 方法", "options方法", "trace 方法", "trace方法"}, "OPTIONS / TRACE 方法启用不收取"},
	{"缺安全响应头", []string{"安全头", "安全响应头", "响应头缺失", "x-frame-options", "csp 缺失"}, "缺少安全头属无危"},
	{"混合内容", []string{"混合内容", "mixed content"}, "混合内容问题不收取"},
	{"用户名枚举", []string{"用户名枚举", "用户枚举"}, "登录/找回密码/注册页的用户名枚举不收取"},
	{"账户锁定与密码策略", []string{"账户锁定", "账号锁定", "密码复杂度", "密码策略", "重置链接过期"}, "账户锁定、密码复杂度、恢复策略类问题不收取"},
	{"SSL / TLS / SSH 配置", []string{"ssl配置", "tls配置", "ssh配置", "弱加密套件"}, "不安全的 SSL/TLS 或 SSH 配置不收取"},
	{"邮件配置（SPF/DKIM/DMARC）", []string{"spf", "dkim", "dmarc", "邮件配置"}, "邮件配置类问题不收取"},
	{"公开登录面板", []string{"公开登录面板", "登录面板暴露"}, "可公开访问的登录面板不收取"},
	{"注册缺邮箱验证", []string{"缺邮箱验证", "未验证邮箱", "注册无验证"}, "注册时缺少邮箱验证不收取"},
	{"已知脆弱组件", []string{"已知脆弱库", "组件版本过低", "存在已知漏洞的组件"}, "仅指出使用了已知易受攻击的库、没有利用证明的不收取"},
	{"内容欺骗 / 文本注入", []string{"内容欺骗", "文本注入"}, "内容欺骗与文本注入属无危"},
	{"无敏感操作的 CSRF", []string{"csrf", "跨站请求伪造"}, "添加/删除购物车、登录退出、点赞关注这类无敏感操作的 CSRF 属无危"},
	{"无意义点击劫持", []string{"点击劫持", "clickjacking", "iframe 嵌套"}, "没有有效 exp 的点击劫持属无危"},
	{"HTTP 请求夹带", []string{"smuggling", "请求夹带"}, "不可进一步利用的请求夹带走无危"},
	{"CORS 无敏感数据", []string{"cors 配置", "cors配置", "跨域配置过松"}, "需要用户交互才能利用、且拿不到敏感数据的 CORS 配方属无危"},
	{"无法利用的爆破", []string{"无法利用的爆破", "爆破无结果"}, "无法进一步利用的账号密码爆破属无危"},
	{"功能缺陷 / 乱码", []string{"功能缺陷", "页面乱码", "样式错乱", "兼容性问题"}, "产品功能缺陷、乱码、样式问题不收取"},
	{"不可重现", []string{"无法重现", "不可重现", "无法复现"}, "无法重现的问题不收取"},
	{"30 天内 0day", []string{"0day", "零日"}, "最近披露的 0day 需要 30 天冷静期，冷静期内不收取"},
}

// enterpriseGradeTable 按 OPPO OSRC V5.0「3.1 Web 应用安全漏洞」的四档定义给基准等级。
var enterpriseGradeTable = []struct {
	name string
	keys []string
	rank int
}{
	{"服务器权限获取", []string{"rce", "命令执行", "代码执行", "命令注入", "代码注入", "code execution", "反序列化", "缓冲区溢出", "webshell", "getshell", "任意文件上传", "文件上传"}, 4},
	{"核心数据整体落地", []string{"核心库", "核心数据库", "核心 db", "资金", "身份库", "交易库", "订单库", "大量敏感信息"}, 4},
	{"任意账号操作", []string{"任意账号", "任意用户登录", "任意用户密码", "任意用户改密", "账号接管", "用户接管", "任意账号登录"}, 4},
	{"绕过认证 / 后台弱口令", []string{"绕过认证", "认证绕过", "登录绕过", "后台弱口令", "后台弱密码", "未授权访问", "unauthorized", "默认口令"}, 3},
	{"SQL 注入", []string{"sql注入", "sql 注入", "sql injection", "盲注"}, 3},
	{"源码 / 密钥泄漏", []string{"源码泄露", "源码压缩包", "源代码泄露", "加密算法可逆", "明文存储", "硬编码", "github泄露", "api摘要"}, 3},
	{"SSRF（内网）", []string{"ssrf", "服务端请求伪造"}, 3},
	{"越权", []string{"越权", "idor", "对象引用", "权限提升", "提权"}, 2},
	{"存储型 XSS", []string{"存储型xss", "存储型 xss", "stored xss"}, 2},
	{"CSRF", []string{"csrf", "跨站请求伪造"}, 2},
	{"路径遍历 / 明文传输", []string{"路径遍历", "目录遍历", "lfi", "文件读取", "明文传输"}, 2},
	{"验证码逻辑 / 爆破", []string{"验证码", "撞库", "爆破", "暴力破解"}, 2},
	{"反射型 XSS", []string{"反射型xss", "反射xss", "reflected xss", "dom-xss", "dom xss"}, 1},
	{"轻微信息泄漏", []string{"信息泄露", "phpinfo", "svn", "日志打印", "配置信息", "备份文件"}, 1},
	{"URL 跳转", []string{"开放重定向", "open redirect", "url跳转", "任意跳转"}, 1},
	{"短信轰炸", []string{"短信轰炸", "验证码轰炸", "邮件轰炸"}, 1},
}

// enterpriseSpecial 处理 OPPO 口径里「同名类别、按证据分档」的情况。
func enterpriseSpecial(class, evidence string) (int, string, bool) {
	lowClass := strings.ToLower(class)
	blob := strings.ToLower(class + " " + evidence)
	// 「非核心库」里的「核心库」不是核心库 —— 先把它抹掉再判断。
	blobCore := strings.ReplaceAll(strings.ReplaceAll(blob, "非核心库", ""), "非核心数据库", "")
	// 只有证据能说明「落在核心库上」时，SQL 注入才够严重；单看类别一律高危。
	if strings.Contains(lowClass, "注入") || strings.Contains(lowClass, "injection") {
		if strings.Contains(blobCore, "核心库") || strings.Contains(blobCore, "核心数据库") || strings.Contains(blobCore, "核心 db") ||
			strings.Contains(blobCore, "资金") || strings.Contains(blobCore, "身份库") || strings.Contains(blobCore, "交易库") ||
			strings.Contains(blobCore, "订单库") {
			return 4, "OPPO 口径：核心库（资金 / 身份 / 交易相关）注入 基准 严重", true
		}
	}
	switch {
	case strings.Contains(blob, "越权") || strings.Contains(blob, "idor"):
		if strings.Contains(blob, "改密") || strings.Contains(blob, "修改密码") || strings.Contains(blob, "资金") ||
			strings.Contains(blob, "订单") && strings.Contains(blob, "操作") || strings.Contains(blob, "重要信息") ||
			strings.Contains(blob, "重要配置") {
			return 3, "OPPO 口径：越权做敏感操作（改密 / 资金 / 订单 / 重要配置）基准 高危", true
		}
		return 2, "OPPO 口径：一般越权（查看普通用户信息、改评论属性）基准 中危", true
	case strings.Contains(blob, "存储型"):
		if strings.Contains(blob, "cookie") || strings.Contains(blob, "自动传播") || strings.Contains(blob, "认证凭据") {
			return 3, "OPPO 口径：可自动传播并获取 Cookie 的重要页面存储型 XSS 基准 高危", true
		}
		return 2, "OPPO 口径：需交互的存储型 XSS 基准 中危", true
	case strings.Contains(blob, "ssrf"):
		// 只是「探测内网存活」够不到高危；要能拿到大量内网敏感信息才算。
		if strings.Contains(blob, "内网敏感") || strings.Contains(blob, "大量") || strings.Contains(blob, "元数据") ||
			strings.Contains(blob, "169.254") || strings.Contains(blob, "临时凭证") || strings.Contains(blob, "云凭证") {
			return 3, "OPPO 口径：可获取大量内网敏感信息的 SSRF 基准 高危", true
		}
		return 2, "OPPO 口径：普通 SSRF 基准 中危", true
	}
	return 0, "", false
}

// policyGradeTable 选表。
func policyGradeTable(policy string) []struct {
	name string
	keys []string
	rank int
} {
	if policy == "enterprise" {
		return enterpriseGradeTable
	}
	return eduGradeTable
}

// PolicyName 给前端与报告用的档位名字。
func PolicyName(policy string) string {
	if policy == "enterprise" {
		return "企业 SRC（OPPO OSRC V5.0）"
	}
	return "EduSRC"
}

// PolicyGrade 是 EduSRC 口径的便捷入口。
func PolicyGrade(vulnclass string) (int, string) {
	return PolicyGradeFor("edu", vulnclass, "")
}

// PolicyGradeFor 按所选 SRC 口径给基准等级。
func PolicyGradeFor(policy, vulnclass, evidence string) (int, string) {
	low := strings.ToLower(vulnclass)
	if policy == "enterprise" {
		if rank, why, ok := enterpriseSpecial(vulnclass, evidence); ok {
			return rank, why
		}
	}
	for _, row := range policyGradeTable(policy) {
		for _, k := range row.keys {
			if strings.Contains(low, k) {
				return row.rank, PolicyName(policy) + " 口径：" + row.name + " 基准 " + RankName(row.rank)
			}
		}
	}
	return 2, "未命中已知类别，按中危处理（要上调就补利用结果证据）"
}

// RankName 把 1..4 映射成中文档位。
func RankName(rank int) string {
	switch rank {
	case 4:
		return "严重"
	case 3:
		return "高危"
	case 2:
		return "中危"
	default:
		return "低危"
	}
}

// 敏感信息泄露的"死规矩"四类，只有命中才算敏感信息泄露，且按量级定档。
var strictSensitiveKeys = []string{"身份证", "大头照", "人脸", "照片", "密码哈希", "口令散列", "bcrypt", "argon2", "$2a$", "$2b$", "$2y$", "$5$", "$6$", "md5哈希", "密码明文"}

// StrictSensitive 判断证据里是否出现四类死规矩数据，并给出建议档位。
func StrictSensitive(evidence string) (bool, int) {
	low := strings.ToLower(evidence)
	hit := 0
	for _, k := range strictSensitiveKeys {
		if strings.Contains(low, k) {
			hit++
		}
	}
	if hit == 0 {
		return false, 0
	}
	// 数量无法从文本精确统计，按"有实证"给高危；模型想升严重必须自己贴出总量与样本。
	return true, 3
}

func rankOfSeverity(name string) int {
	if r, ok := severityRanks[strings.ToLower(strings.TrimSpace(name))]; ok {
		return r
	}
	return rankMedium
}

func joinNote(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "；" + b
}
