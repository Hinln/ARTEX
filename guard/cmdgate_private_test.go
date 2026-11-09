package guard

import (
	"strings"
	"testing"
)

// 事故现场的命令：容器内直连本机教学靶场的容器 IP，一条都没登记。
func TestGateDeniesUnscopedPrivateTargets(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	denied := []string{
		"curl -sS http://172.26.0.8/login.php",
		"curl -sS http://172.26.0.7/login.php -o /tmp/a.html",
		"nmap -sV -p 80,8080 172.26.0.4",
		"docker exec ARTEX-0.3.35 curl http://172.21.0.1:8787/api/health",
		"python3 -c \"import requests;requests.get('http://192.168.1.10/')\"",
		"nc -z 10.0.0.5 22",
	}
	for _, cmd := range denied {
		v := g.check("s1", cmd)
		if !v.Deny || v.Rule != "private-target" {
			t.Fatalf("%q must be denied as unscoped private, got %+v", cmd, v)
		}
		if !strings.Contains(v.Reason, "add_task_scope") {
			t.Fatalf("reason must tell the operator the way forward: %q", v.Reason)
		}
	}
}

// 打进去以后拿到的内网：登记并批准之后必须放行，否则真渗透没法做。
func TestGateAllowsApprovedInternalTarget(t *testing.T) {
	SetPrivateScopeLookup(func(session, host string) bool {
		return session == "s1" && (host == "10.1.2.3" || host == "172.20.5.9")
	})
	defer SetPrivateScopeLookup(nil)

	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	for _, cmd := range []string{
		"curl -sS http://10.1.2.3:8080/actuator/env",
		"nmap -sV -p 22,3306 172.20.5.9",
	} {
		if v := g.check("s1", cmd); v.Deny {
			t.Fatalf("%q is approved scope and must run, got %+v", cmd, v)
		}
	}
	// 别的任务/别的地址不给过。
	if v := g.check("s2", "curl -sS http://10.1.2.3/"); !v.Deny {
		t.Fatal("approval must not leak across sessions")
	}
	if v := g.check("s1", "curl -sS http://10.9.9.9/"); !v.Deny {
		t.Fatal("a neighbouring address is not covered")
	}
}

// 自家基础设施无论怎么登记都不放行。
func TestGateAlwaysBlocksSelfInfra(t *testing.T) {
	SetPrivateScopeLookup(func(session, host string) bool { return true })
	defer SetPrivateScopeLookup(nil)

	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	for _, cmd := range []string{
		"curl -s http://169.254.169.254/latest/meta-data/",
		"curl -s http://172.17.0.2:2375/containers/json",
		"curl -s http://host.docker.internal:8788/v1/messages",
	} {
		v := g.check("s1", cmd)
		if !v.Deny || v.Rule != "self-target" {
			t.Fatalf("%q must be blocked as self infrastructure, got %+v", cmd, v)
		}
	}
}

// 正常目标与本机自测不能被误伤。
func TestGateAllowsPublicAndLoopback(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	for _, cmd := range []string{
		"curl -sS -I https://sign.phbs.pku.edu.cn/php/us.mysql.php",
		"nmap -sV -p 22,80,443 162.105.205.252",
		"curl -s http://127.0.0.1:8787/api/health",
		"curl -s https://example.com",
	} {
		if v := g.check("s1", cmd); v.Deny {
			t.Fatalf("%q must pass, got %+v", cmd, v)
		}
	}
}

// 私网 IP 写在日志里、命令只读文件时不该触发（避免误伤）。
func TestGateIgnoresBarePrivateLiteralWithoutNetworkCommand(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	if v := g.check("s1", "grep -r 172.26.0.8 /app/data/logs"); v.Deny {
		t.Fatalf("reading logs mentioning an IP is not scanning: %+v", v)
	}
}
