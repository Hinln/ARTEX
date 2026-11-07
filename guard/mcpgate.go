package guard

import (
	"encoding/json"
	"strings"
)

// MCP tools are the last unguarded way out of the sandbox: they are not Bash,
// so the command rules never saw them, and their arguments are free-form JSON,
// so the host budget never saw them either. A yaklang/exec-style server could
// run `nmap -p-` through an MCP call while every rule pack stayed silent.
//
// mcpCall looks at the raw arguments of an mcp__<server>__<tool> call and digs
// out the two things the gate understands: command strings and target hosts.

// mcpCommandKeys are keys whose string value is executed somewhere downstream.
var mcpCommandKeys = map[string]bool{
	"command": true, "cmd": true, "shell": true, "script": true, "code": true,
	"payload": true, "args": true, "argv": true, "exec": true, "query": true,
	"expression": true, "sql": true, "path": true, "file": true, "urls": true,
}

// mcpHostKeys are keys whose value names something on the network.
var mcpHostKeys = map[string]bool{
	"url": true, "host": true, "target": true, "endpoint": true, "address": true,
	"ip": true, "domain": true, "hostname": true, "base_url": true, "baseurl": true,
	"uri": true, "dest": true, "destination": true, "server": true,
}

func mcpCall(toolName string, raw json.RawMessage) (cmds []string, hosts []string) {
	if !strings.HasPrefix(toolName, "mcp__") || len(raw) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		// 解析不了就把原文当命令看，宁可多拦一次。
		if s := strings.TrimSpace(string(raw)); s != "" && s != "null" {
			cmds = append(cmds, s)
		}
		return cmds, nil
	}
	walkMCP(v, "", &cmds, &hosts, 0)
	return cmds, hosts
}

func walkMCP(v any, key string, cmds, hosts *[]string, depth int) {
	if depth > 6 {
		return
	}
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			walkMCP(child, strings.ToLower(k), cmds, hosts, depth+1)
		}
	case []any:
		// MCP servers often pass argv as an array: ["nmap","-p-","10.0.0.5"].
		// Joined it is one command line the rules can actually read; element by
		// element the flags land on the wrong side of a ";" and slip through.
		if mcpCommandKeys[key] {
			parts := make([]string, 0, len(t))
			allStrings := len(t) > 0
			for _, child := range t {
				str, ok := child.(string)
				if !ok {
					allStrings = false
					break
				}
				parts = append(parts, str)
			}
			if allStrings {
				joined := strings.Join(parts, " ")
				if looksLikeCommand(joined) {
					*cmds = append(*cmds, joined)
					for _, h := range targetHosts(joined) {
						*hosts = append(*hosts, h)
					}
					return
				}
			}
		}
		for _, child := range t {
			walkMCP(child, key, cmds, hosts, depth+1)
		}
	case string:
		s := strings.TrimSpace(t)
		if s == "" {
			return
		}
		if mcpHostKeys[key] || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
			if h := mcpHostFromValue(s); h != "" {
				*hosts = append(*hosts, h)
			}
		}
		if s == "" || strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
			return
		}
		if mcpCommandKeys[key] && looksLikeCommand(s) {
			*cmds = append(*cmds, s)
			for _, h := range targetHosts(s) {
				*hosts = append(*hosts, h)
			}
			return
		}
		if strings.ContainsAny(s, " ;|&") && looksLikeCommand(s) {
			*cmds = append(*cmds, s)
			for _, h := range targetHosts(s) {
				*hosts = append(*hosts, h)
			}
		}
	}
}

// looksLikeCommand keeps file paths and plain identifiers out of the gate: a
// rule only matters when the string carries a real invocation.
func looksLikeCommand(s string) bool {
	if len(s) < 4 {
		return false
	}
	for _, tool := range []string{
		"nmap", "masscan", "zmap", "hydra", "sqlmap", "ffuf", "gobuster", "dirsearch",
		"feroxbuster", "wfuzz", "dirb", "ncrack", "medusa", "patator", "curl", "wget",
		"nc ", "ncat", "socat", "bash", "sh -", "python", "node ", "perl", "ruby",
	} {
		if strings.Contains(s, tool) {
			return true
		}
	}
	return false
}

func mcpHostFromValue(s string) string {
	if i := strings.Index(s, "://"); i >= 0 {
		rest := s[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[:j]
		}
		if k := strings.Index(rest, "@"); k >= 0 {
			rest = rest[k+1:]
		}
		if k := strings.Index(rest, ":"); k >= 0 {
			rest = rest[:k]
		}
		if rest == "" || strings.HasPrefix(rest, "localhost") || strings.HasPrefix(rest, "127.") {
			return ""
		}
		return rest
	}
	hs := targetHosts(s)
	if len(hs) == 0 {
		return ""
	}
	return hs[0]
}
