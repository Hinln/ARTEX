package guard

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMCPCallExtractsCommand(t *testing.T) {
	raw := json.RawMessage(`{"command":"nmap -n -Pn -sS -p- 10.0.0.5","timeout":30}`)
	cmds, hosts := mcpCall("mcp__yaklang__exec", raw)
	if len(cmds) != 1 || !strings.Contains(cmds[0], "nmap") {
		t.Fatalf("command not extracted: %v", cmds)
	}
	if len(hosts) == 0 {
		t.Fatalf("host not extracted from command")
	}
}

func TestMCPCallExtractsHost(t *testing.T) {
	raw := json.RawMessage(`{"url":"https://target.test/admin","method":"GET"}`)
	_, hosts := mcpCall("mcp__browser__navigate", raw)
	if len(hosts) != 1 || hosts[0] != "target.test" {
		t.Fatalf("host = %v", hosts)
	}
}

func TestMCPCallIgnoresPlainArguments(t *testing.T) {
	raw := json.RawMessage(`{"project":"demo","note":"/app/data/file.txt","limit":10}`)
	cmds, hosts := mcpCall("mcp__pentrack__asset_add", raw)
	if len(cmds) != 0 || len(hosts) != 0 {
		t.Fatalf("plain bookkeeping flagged: cmds=%v hosts=%v", cmds, hosts)
	}
}

func TestMCPCallSkipsNonMCPTools(t *testing.T) {
	raw := json.RawMessage(`{"command":"nmap -p- x"}`)
	if cmds, _ := mcpCall("Bash", raw); len(cmds) != 0 {
		t.Fatalf("non-MCP tool routed through mcpCall")
	}
}

func TestMCPHardGateBlocksFullPortSweep(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	raw := json.RawMessage(`{"args":["nmap","-n","-Pn","-sS","-p-","10.0.0.5"]}`)
	cmds, _ := mcpCall("mcp__yaklang__exec", raw)
	if len(cmds) == 0 {
		t.Fatalf("array-of-args command not extracted")
	}
	if v := g.check("1", strings.Join(cmds, " ; ")); !v.Deny || v.Rule != "full-port-sweep" {
		t.Fatalf("argv array full-port sweep not blocked: %+v (cmds=%v)", v, cmds)
	}
}

func TestMCPHardGateBlocksNestedCommand(t *testing.T) {
	g := newTestGate(func(c *gateConfig) { c.BreakerN = 99 })
	raw := json.RawMessage(`{"step":{"command":"sqlmap -u https://x.test/?id=1 --dump"}}`)
	cmds, _ := mcpCall("mcp__yaklang__exec", raw)
	if len(cmds) == 0 {
		t.Fatalf("nested command not extracted")
	}
	if v := g.check("1", strings.Join(cmds, " ; ")); !v.Deny || v.Rule != "credential-bruteforce" {
		t.Fatalf("sqlmap --dump not blocked: %+v", v)
	}
}
