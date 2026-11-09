package guard

import (
	"strings"
	"testing"
)

// 事故现场的命令：容器内直连本机教学靶场的容器 IP。
func TestGateDeniesPrivateTargets(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	denied := []string{
		"curl -sS http://172.26.0.8/login.php",
		"curl -sS http://172.26.0.7/login.php -o /tmp/a.html",
		"nmap -sV -p 80,8080 172.26.0.4",
		"docker exec ARTEX-0.3.35 curl http://172.21.0.1:8787/api/health",
		"python3 -c \"import requests;requests.get('http://192.168.1.10/')\"",
		"curl -s http://169.254.169.254/latest/meta-data/",
		"nc -z 10.0.0.5 22",
	}
	for _, cmd := range denied {
		v := g.check("s1", cmd)
		if !v.Deny || v.Rule != "private-target" {
			t.Fatalf("%q must be denied by private-target, got %+v", cmd, v)
		}
		if !strings.Contains(v.Reason, "RFC1918") {
			t.Fatalf("reason must say why: %q", v.Reason)
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

// 私网 IP 写在配置文件里、命令只引用文件时不该触发（避免误伤）。
func TestGateIgnoresBarePrivateLiteralWithoutNetworkCommand(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	if v := g.check("s1", "grep -r 172.26.0.8 /app/data/logs"); v.Deny {
		t.Fatalf("reading logs mentioning an IP is not scanning: %+v", v)
	}
}
