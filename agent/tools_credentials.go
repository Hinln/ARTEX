package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	actool "github.com/Autumn-27/norma/tool"
)

// list_credentials 把已经抽取好的账号交给 worker。
//
// 没有它，authz_diff 的 identities 只能靠模型手抄 header：它看不到库里抽出来的
// 账号，越权测试就只能凭想象编一个 Cookie，跑出来自然全是「请求失败」。
// 这里只读本任务已确认的凭据，不做任何额外抓取。
func (t *ToolSet) listCredentials() actool.CoreTool {
	return readTool("list_credentials", "列出本任务已抽取并确认的登录凭据（用户名/密码/token/cookie/登录入口）。做越权、越账号、水平垂直权限测试前先调它，用真实账号，不要手编 Cookie。只读，不发请求。",
		obj(map[string]any{
			"match": str("可选：只返回登录入口或备注里包含该字符串的账号（填目标主机名即可过滤）"),
			"limit": map[string]any{"type": "integer", "description": "最多返回条数，默认 20，上限 50"},
			"show_secrets": map[string]any{"type": "boolean", "description": "是否返回密码/token/cookie 原文，默认 true（越权测试需要真实凭据）"},
		}), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Match       string `json:"match"`
				Limit       int    `json:"limit"`
				ShowSecrets *bool  `json:"show_secrets"`
			}
			if len(in) > 0 {
				if err := json.Unmarshal(in, &a); err != nil {
					return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
				}
			}
			if t.ts == nil {
				return actool.Errorf("list_credentials 不可用：本会话没有任务存储"), nil
			}
			if a.Limit <= 0 || a.Limit > 50 {
				a.Limit = 20
			}
			show := true
			if a.ShowSecrets != nil {
				show = *a.ShowSecrets
			}
			creds, err := t.ts.TaskCredentials(ctx, a.Limit)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("读取凭据失败: %v", err)), nil
			}
			match := strings.ToLower(strings.TrimSpace(a.Match))
			var b strings.Builder
			n := 0
			for _, c := range creds {
				blob := strings.ToLower(c.Label + " " + c.Endpoint + " " + c.Username + " " + c.Protocol)
				if match != "" && !strings.Contains(blob, match) {
					continue
				}
				n++
				fmt.Fprintf(&b, "%d. %s\n", n, nzDash(c.Label, "未命名凭据"))
				if c.Endpoint != "" {
					fmt.Fprintf(&b, "   入口: %s\n", c.Endpoint)
				}
				if c.Protocol != "" {
					fmt.Fprintf(&b, "   协议: %s\n", c.Protocol)
				}
				if c.Username != "" {
					fmt.Fprintf(&b, "   用户名: %s\n", c.Username)
				}
				b.WriteString(secretLine("密码", c.Password, show))
				b.WriteString(secretLine("Token", c.Token, show))
				b.WriteString(secretLine("Cookie", c.Cookie, show))
			}
			if n == 0 {
				if match == "" {
					return actool.Text("本任务还没有已抽取的凭据。先从流量/配置文件里提取账号（intelligence），或手工补录后再做越权测试。"), nil
				}
				return actool.Text("没有匹配「" + match + "」的凭据。去掉 match 看全部，或确认该目标是否真的拿到了账号。"), nil
			}
			fmt.Fprintf(&b, "\n用法：把这些凭据原样填进 authz_diff 的 identities（每个身份一个 entry，headers 里带 Cookie 或 Authorization），owner 指定业务属主，其余身份重放同一请求做比对。\n")
			return actool.Text(b.String()), nil
		})
}

func secretLine(name, v string, show bool) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	if !show {
		return "   " + name + ": （已隐藏，去掉 show_secrets=false 看原文）\n"
	}
	if len(v) > 400 {
		v = v[:400] + "…（截断）"
	}
	return "   " + name + ": " + v + "\n"
}

func nzDash(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}
