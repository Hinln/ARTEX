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
	blob := strings.ToLower(vulnclass + " " + summary)
	for _, rule := range severitiesNotAccepted {
		for _, k := range rule.keys {
			if strings.Contains(blob, k) {
				return rule.name + "：" + rule.reason, true
			}
		}
	}
	return "", false
}

// PolicyGrade 按类别给基准等级，返回 rank 与中文说明。
func PolicyGrade(vulnclass string) (int, string) {
	low := strings.ToLower(vulnclass)
	for _, row := range eduGradeTable {
		for _, k := range row.keys {
			if strings.Contains(low, k) {
				return row.rank, "EduSRC 口径：" + row.name + " 基准 " + RankName(row.rank)
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
