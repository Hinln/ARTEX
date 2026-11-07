package agent

import "testing"

func TestPolicyRejectsWhatSrcRefuses(t *testing.T) {
	rejected := []string{"反射型XSS", "Self-XSS", "phpinfo泄露", "内网IP泄露", "DoS", "CSRF", "Open Redirect", "CORS 配置过松", "硬编码密钥泄露"}
	for _, c := range rejected {
		if _, ok := RejectReason(c, ""); !ok {
			t.Fatalf("%s should be rejected by policy", c)
		}
	}
	kept := []string{"SQL注入", "命令执行", "水平越权", "文件读取", "存储型XSS", "未授权访问"}
	for _, c := range kept {
		if why, ok := RejectReason(c, ""); ok {
			t.Fatalf("%s wrongly rejected: %s", c, why)
		}
	}
}

func TestPolicyGradeMatchesEduTable(t *testing.T) {
	cases := map[string]int{
		"命令执行":       4,
		"上传webshell": 4,
		"SQL注入":      3,
		"认证绕过":       3,
		"水平越权":       2,
		"SSRF":       2,
		"存储型XSS":     2,
		"信息泄露":       1,
	}
	for class, want := range cases {
		got, why := PolicyGrade(class)
		if got != want {
			t.Fatalf("%s graded %d (%s), want %d", class, got, why, want)
		}
	}
}

func TestSeverityCannotExceedPolicy(t *testing.T) {
	// 普通 SQL 注入：模型写严重，平台口径是高危；证据是报错，够高危，不该被压到中危。
	got, changed, note := CalibrateSeverity("SQL注入", "You have an error in your SQL syntax near '1''", "严重")
	if got != "high" || !changed {
		t.Fatalf("got %q changed=%v note=%q", got, changed, note)
	}
	// 命令执行却只有一条 HTTP 200：证据上限压到中危，防止"说能打但不给回显"。
	got, changed, _ = CalibrateSeverity("命令执行", "HTTP/1.1 200 OK\r\nContent-Type: text/html\r\n\r\nok", "严重")
	if got != "medium" || !changed {
		t.Fatalf("RCE without echo graded %q", got)
	}
	// 有 root 回显才允许严重。
	got, _, _ = CalibrateSeverity("命令执行", "uid=0(root) gid=0(root) groups=0(root)", "严重")
	if got != "critical" {
		t.Fatalf("real RCE graded %q", got)
	}
}

func TestMustVerifyCoversExecutableClaims(t *testing.T) {
	for _, c := range []string{"命令执行", "SQL注入", "任意文件上传", "水平越权", "SSRF", "文件读取"} {
		if !MustVerify(c) {
			t.Fatalf("%s must be reproduced", c)
		}
	}
	for _, c := range []string{"信息泄露", "配置缺陷", "弱口令"} {
		if MustVerify(c) {
			t.Fatalf("%s should not force reproduction", c)
		}
	}
}

func TestStrictSensitiveNeedsTheFourKinds(t *testing.T) {
	if ok, _ := StrictSensitive("共返回 3 条：姓名 张三 手机号 138****"); ok {
		t.Fatalf("name/phone is not one of the four strict kinds")
	}
	if ok, rank := StrictSensitive("响应含 110101199003071234 身份证号"); !ok || rank != 3 {
		t.Fatalf("id card should be strict high, got %v %d", ok, rank)
	}
	if ok, _ := StrictSensitive("password_hash: $2a$10$abcdefghijklmnopqrstuv"); !ok {
		t.Fatalf("bcrypt hash should count")
	}
}

func TestEnterprisePolicyKeepsReflectedXSSAsLow(t *testing.T) {
	// OPPO 把反射型 XSS 定为低危，而 EduSRC 直接不收 —— 换口径不能一律套 EduSRC。
	if _, ok := RejectReasonFor("edu", "反射型XSS", ""); !ok {
		t.Fatal("edu should reject reflected xss")
	}
	if why, ok := RejectReasonFor("enterprise", "反射型XSS", ""); ok {
		t.Fatalf("enterprise should keep reflected xss, rejected with: %s", why)
	}
	if rank, why := PolicyGradeFor("enterprise", "反射型XSS", ""); rank != 1 {
		t.Fatalf("enterprise reflected xss rank = %d (%s), want 1", rank, why)
	}
}

func TestEnterprisePolicyRejectsWhatOppoRefuses(t *testing.T) {
	cases := []string{
		"内网IP泄露", "扫描器报告命中", "中间件版本号", "Self-XSS", "缺少安全响应头",
		"用户名枚举", "SSL配置不当", "混合内容", "公开登录面板", "无法重现的漏洞",
	}
	for _, c := range cases {
		if why, ok := RejectReasonFor("enterprise", c, ""); !ok {
			t.Fatalf("%s should be rejected under enterprise policy", c)
		} else if why == "" {
			t.Fatalf("%s rejected without a reason", c)
		}
	}
}

func TestEnterprisePolicyGradesByOppoDefinitions(t *testing.T) {
	cases := []struct {
		class    string
		evidence string
		rank     int
	}{
		{"SQL注入", "核心库 资金 订单接口 dump 出 30 万条", 4},
		{"命令执行", "uid=0(root)", 4},
		{"任意账号密码修改", "", 4},
		{"SQL注入", "非核心库报错注入", 3},
		{"SSRF", "打到 169.254.169.254 拿到临时凭证", 3},
		{"SSRF", "仅探测内网存活", 2},
		{"越权", "改密接口越权重置他人密码", 3},
		{"越权", "查看他人普通订单列表", 2},
		{"存储型XSS", "可在重要页面自动传播并获取 Cookie", 3},
		{"存储型XSS", "需要受害者交互才会触发", 2},
		{"路径遍历", "读取 /etc/passwd", 2},
		{"短信轰炸", "单 IP 半小时 200 条", 1},
	}
	for _, c := range cases {
		if got, why := PolicyGradeFor("enterprise", c.class, c.evidence); got != c.rank {
			t.Fatalf("%s / %q rank = %d (%s), want %d", c.class, c.evidence, got, why, c.rank)
		}
	}
}

func TestEnterpriseCalibrationCapsAtPolicy(t *testing.T) {
	// 声明 critical，但 OPPO 口径下反射型 XSS 只能到低危。
	got, changed, note := CalibrateSeverityFor("enterprise", "反射型XSS", "HTTP/1.1 200 OK\nContent-Type: text/html", "critical")
	if got != "low" || !changed || note == "" {
		t.Fatalf("enterprise calibration = %s changed=%v note=%q, want low", got, changed, note)
	}
}

func TestPolicyNameIsReadable(t *testing.T) {
	if PolicyName("enterprise") == PolicyName("edu") {
		t.Fatal("policy names must differ")
	}
	if PolicyName("") != "EduSRC" {
		t.Fatalf("default policy name = %q", PolicyName(""))
	}
}
