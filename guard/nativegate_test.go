package guard

import (
	"encoding/json"
	"testing"
)

func TestNativeNetHostsReadsTargets(t *testing.T) {
	cases := []struct {
		tool string
		body string
		want string
	}{
		{"http_request", `{"url":"https://app.example.com/api/x"}`, "app.example.com"},
		{"tls_info", `{"host":"internal.corp.example.cn","port":443}`, "internal.corp.example.cn"},
		{"dns_lookup", `{"host":"example.org","types":["A"]}`, "example.org"},
		{"port_probe", `{"host":"10.0.0.1","ports":"22,80"}`, "10.0.0.1"},
		{"parse_response", `{"text":"HTTP/1.1 200 OK"}`, ""},
	}
	for _, c := range cases {
		got := nativeNetHosts(c.tool, json.RawMessage(c.body))
		if c.want == "" {
			if len(got) != 0 {
				t.Fatalf("%s: expected no host, got %v", c.tool, got)
			}
			continue
		}
		if len(got) != 1 || got[0] != c.want {
			t.Fatalf("%s: hosts = %v want %s", c.tool, got, c.want)
		}
	}
}

func TestNativeGateSharesHostBudget(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.RateRPM = 2; c.BreakerN = 99 })
	hosts := []string{"app.example.com"}
	for i := 0; i < 2; i++ {
		if v := g.checkHosts("1", hosts); v.Deny {
			t.Fatalf("call %d must pass: %+v", i+1, v)
		}
	}
	v := g.checkHosts("1", hosts)
	if !v.Deny || v.Rule != "rate-limit" {
		t.Fatalf("third call must be rate limited, got %+v", v)
	}
}

func TestNativeGateBreakerStopsEverything(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.BreakerN = 2 })
	hosts := []string{"a.example.com"}
	if v := g.checkHosts("7", hosts); v.Deny {
		t.Fatalf("first call must pass: %+v", v)
	}
	g.deny("7", "full-port-sweep", "x")
	g.deny("7", "full-port-sweep", "x")
	v := g.checkHosts("7", hosts)
	if !v.Deny || v.Rule != "breaker-open" {
		t.Fatalf("breaker must stop native calls too, got %+v", v)
	}
}

func TestNativeGateConcurrencyReleased(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.Concurrency = 1; c.BreakerN = 99 })
	hosts := []string{"b.example.com"}
	if v := g.checkHosts("2", hosts); v.Deny {
		t.Fatalf("first call must pass: %+v", v)
	}
	g.acquireHosts(hosts)
	if v := g.checkHosts("2", hosts); !v.Deny || v.Rule != "concurrency" {
		t.Fatalf("in-flight cap must hold, got %+v", v)
	}
	g.doneHosts(hosts)
	if v := g.checkHosts("2", hosts); v.Deny {
		t.Fatalf("slot released, call must pass: %+v", v)
	}
}
