package server

// 信息搜集（Recon）配置：FOFA key 池。
//
// 为什么不复用 /api/mcp 直接改 env：key 池是用户面向的概念（增删单个 key、
// 看每个 key 的状态），而 MCP 那一层只认一个 FOFA_KEYS 逗号串。让前端拼接
// JSON 字符串既容易写错，也会把明文 key 混进通用 MCP 配置里。这里给它一个
// 一等接口：key 列表进列表出，落库时再由本文件原子地写回 MCP env。
//
// 安全约定：
//   - 读取接口只返回脱敏 key（前 4 + 后 4），从不回显完整 key。
//   - 保存时按索引合并：留空的 key 视为「保持原值」，这样前端可以在不知道
//     明文的情况下改顺序/删条目/加新条目。

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
)

const reconFOFAServer = "fofa"
// reconFOFAScriptPath reports where the FOFA MCP server script lives. The image
// ships it at /app/mcp; ARTEX_FOFA_SCRIPT overrides the path so a host run can
// point at the checkout, and a plain checkout is probed last.
func reconFOFAScriptPath() string {
	if v := strings.TrimSpace(os.Getenv("ARTEX_FOFA_SCRIPT")); v != "" {
		return v
	}
	for _, p := range []string{"/app/mcp/fofa_mcp.py", "mcp/fofa_mcp.py"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "/app/mcp/fofa_mcp.py"
}

// fofaKeyEntry 是前端看到的一条 key。Value 只在写入时使用，读取时永远为空。
type fofaKeyEntry struct {
	Masked string `json:"masked"`
	Value  string `json:"value,omitempty"`
}

type fofaReconConfig struct {
	Enabled      bool           `json:"enabled"`
	Keys         []fofaKeyEntry `json:"keys"`
	MaxAttempts  int            `json:"max_attempts"`
	AccountCool  int            `json:"account_cooldown"`
	NetworkCool  int            `json:"network_cooldown"`
	Installed    bool           `json:"installed"`     // MCP server 行是否存在
	MCPEnabled   bool           `json:"mcp_enabled"`   // MCP 是否启用
	ToolCached   int            `json:"tool_cached"`   // 已发现的工具数
	ScriptExists bool           `json:"script_exists"` // 容器内脚本是否存在（仅提示）
}

func maskKey(k string) string {
	k = strings.TrimSpace(k)
	if len(k) <= 8 {
		return "***"
	}
	return k[:4] + "****" + k[len(k)-4:]
}

// splitKeys 解析 FOFA_KEYS（逗号分隔），去空去重。
func splitKeys(raw string) []string {
	seen := map[string]bool{}
	var out []string
	for _, k := range strings.Split(raw, ",") {
		k = strings.TrimSpace(k)
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, k)
	}
	return out
}

func (s *Server) registerRecon(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/recon/fofa", s.getFOFARecon)
	mux.HandleFunc("POST /api/recon/fofa", s.saveFOFARecon)
}

func (s *Server) getFOFARecon(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	servers, err := pg.ListMCP()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	cfg := fofaReconConfig{MaxAttempts: 3, AccountCool: 1800, NetworkCool: 30}
	for _, m := range servers {
		if m.Name != reconFOFAServer {
			continue
		}
		cfg.Installed = true
		cfg.Enabled = m.Enabled
		cfg.MCPEnabled = m.Enabled
		cfg.ToolCached = len(m.Tools)
		var env map[string]string
		_ = json.Unmarshal(m.Env, &env)
		for _, k := range splitKeys(env["FOFA_KEYS"]) {
			cfg.Keys = append(cfg.Keys, fofaKeyEntry{Masked: maskKey(k)})
		}
		if v := atoiDefault(env["FOFA_MAX_ATTEMPTS"], 0); v > 0 {
			cfg.MaxAttempts = v
		}
		if v := atoiDefault(env["FOFA_POOL_COOLDOWN"], 0); v > 0 {
			cfg.AccountCool = v
		}
		if v := atoiDefault(env["FOFA_NET_COOLDOWN"], 0); v > 0 {
			cfg.NetworkCool = v
		}
	}
	if cfg.Keys == nil {
		cfg.Keys = []fofaKeyEntry{}
	}
	writeJSON(w, 200, cfg)
}

// saveFOFARecon 写入 key 池并把配置同步到 MCP server 行。
//
// keys 数组里 value 为空表示「保留该位置原 key」，因此前端可以在不回显明文
// 的前提下调整池子；显式传 {"masked":"","value":"新key"} 表示追加。
func (s *Server) saveFOFARecon(w http.ResponseWriter, r *http.Request) {
	pg := s.pg(w)
	if pg == nil {
		return
	}
	var body struct {
		Enabled     bool           `json:"enabled"`
		Keys        []fofaKeyEntry `json:"keys"`
		MaxAttempts int            `json:"max_attempts"`
		AccountCool int            `json:"account_cooldown"`
		NetworkCool int            `json:"network_cooldown"`
	}
	if err := decode(r, &body); err != nil {
		writeErr(w, 400, err.Error())
		return
	}

	servers, err := pg.ListMCP()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	var existing *db.MCPServer
	for _, m := range servers {
		if m.Name == reconFOFAServer {
			existing = m
			break
		}
	}
	if existing == nil {
		existing = &db.MCPServer{
			Name:      reconFOFAServer,
			Transport: "stdio",
			Command:   "python3",
			Args:      json.RawMessage(`["` + reconFOFAScriptPath() + `"]`),
			Env:       json.RawMessage(`{}`),
		}
	}
	var env map[string]string
	_ = json.Unmarshal(existing.Env, &env)
	if env == nil {
		env = map[string]string{}
	}
	old := splitKeys(env["FOFA_KEYS"])

	uniq := mergeKeys(old, body.Keys)
	if len(uniq) == 0 {
		writeErr(w, 400, "至少需要配置一个 FOFA key")
		return
	}

	if body.MaxAttempts <= 0 {
		body.MaxAttempts = 3
	}
	if body.MaxAttempts > len(uniq) {
		body.MaxAttempts = len(uniq)
	}
	if body.AccountCool <= 0 {
		body.AccountCool = 1800
	}
	if body.NetworkCool <= 0 {
		body.NetworkCool = 30
	}

	env["FOFA_KEYS"] = strings.Join(uniq, ",")
	env["FOFA_MAX_ATTEMPTS"] = strconv.Itoa(body.MaxAttempts)
	env["FOFA_POOL_COOLDOWN"] = strconv.Itoa(body.AccountCool)
	env["FOFA_NET_COOLDOWN"] = strconv.Itoa(body.NetworkCool)
	rawEnv, _ := json.Marshal(env)
	existing.Env = rawEnv
	existing.Enabled = body.Enabled
	// 固定 stdio 启动参数：用户不该在这里改出跑不起来的配置。
	existing.Transport = "stdio"
	existing.Command = "python3"
	if len(existing.Args) == 0 || string(existing.Args) == "" || string(existing.Args) == "null" {
		existing.Args = json.RawMessage(`["` + reconFOFAScriptPath() + `"]`)
	}
	id, err := pg.SaveMCP(existing)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	existing.ID = id

	// 启用时立刻刷新工具缓存，让 UI 马上显示可用工具；失败只记日志，
	// 因为 stdio 子进程起不来（容器内缺 python 等）不该让保存失败。
	toolCount := 0
	if body.Enabled {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if derr := s.discoverAndCacheMCP(ctx, existing); derr != nil {
			log.Printf("[recon] fofa 工具发现失败: %v", derr)
		} else {
			if list, lerr := pg.MCPToolNames(id); lerr == nil {
				toolCount = len(list)
			}
		}
	}

	log.Printf("[recon] fofa key 池已更新: %d 个 key, enabled=%v, max_attempts=%d",
		len(uniq), body.Enabled, body.MaxAttempts)
	writeJSON(w, 200, map[string]any{
		"ok":          true,
		"key_count":   len(uniq),
		"enabled":     body.Enabled,
		"tool_cached": toolCount,
	})
}

// mergeKeys 按位置合并 key 池：value 非空用新值，为空沿用同位置的旧 key，
// 越界的空条目丢弃。这样前端在不知道明文的前提下也能增删/重排 key。
func mergeKeys(old []string, in []fofaKeyEntry) []string {
	var keys []string
	for i, e := range in {
		v := strings.TrimSpace(e.Value)
		if v != "" {
			keys = append(keys, v)
			continue
		}
		if i < len(old) {
			keys = append(keys, old[i])
		}
	}
	seen := map[string]bool{}
	var uniq []string
	for _, k := range keys {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		uniq = append(uniq, k)
	}
	return uniq
}
