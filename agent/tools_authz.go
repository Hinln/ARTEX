package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// 越权差分：同一个对象，换身份重放，比响应。
//
// 水平越权靠"两个普通账号拿到同一份数据"暴露，垂直越权靠"低权身份拿到高权响应"暴露。
// 这里不猜 payload，只做确定性对比，所以结论可以写进报告当证据。

const (
	authzMaxIdentities = 8
	authzMaxBody       = 256 << 10
	authzDefaultTime   = 15 * time.Second
	authzSameRatio     = 0.92
	authzSameLenDelta  = 0.08
	authzTrigramRatio  = 0.85
)

var authzDenyMarkers = []string{
	"无权限", "无权访问", "没有权限", "无访问权限", "请先登录", "登录后查看",
	"403 Forbidden", "Access Denied", "Not Authorized", "Forbidden", "Unauthorized",
}

type authzIdentity struct {
	Name    string            `json:"name"`
	Headers map[string]string `json:"headers"`
	Cookie  string            `json:"cookie"`
}

type authzReply struct {
	Name   string
	Status int
	Length int
	Millis int64
	Body   string
	Err    string
}

func (r authzReply) ok() bool { return r.Err == "" && r.Status > 0 && r.Status < 400 }

func (r authzReply) denied() bool {
	low := strings.ToLower(r.Body)
	for _, m := range authzDenyMarkers {
		if strings.Contains(strings.ToLower(r.Body), strings.ToLower(m)) || strings.Contains(low, strings.ToLower(m)) {
			return true
		}
	}
	return r.Status == 401 || r.Status == 403
}

func (t *ToolSet) authzDiff() actool.CoreTool {
	return readTool("authz_diff",
		"越权差分：给出同一个请求和两组以上身份（各自带 Cookie/Token 头），分别重放并逐字段比对状态码、长度、内容相似度。两个不同账号拿到完全一致的内容、或低权身份拿到本应被拒的内容，都会被点出来。用于验证水平/垂直越权，不爆破、不猜 id。",
		obj(map[string]any{
			"url":    str("完整 URL，含对象 id，例如 https://x.com/api/order/1001"),
			"method": icpEnum("GET（默认）或 POST", []string{"GET", "POST"}),
			"body":   str("POST 时的请求体原文"),
			"identities": map[string]any{
				"type":        "array",
				"minItems":    2,
				"maxItems":    authzMaxIdentities,
				"description": "身份列表，至少两个。每项 {name, headers:{...}, cookie}。name 写清是谁，例如 alice-普通用户 / admin / anonymous",
				"items": obj(map[string]any{
					"name":    str("身份名，会出现在比对表里"),
					"cookie":  str("该身份的 Cookie 头原文；没有就留空"),
					"headers": map[string]any{"type": "object", "description": "该身份额外的头，例如 Authorization: Bearer xxx"},
				}, "name"),
			},
			"owner":           str("可选：哪一个 name 是这个资源的合法属主。不给就取第一个身份当基线"),
			"timeout_seconds": map[string]any{"type": "integer", "description": "单请求超时秒数，默认 15，上限 60"},
		}, "url", "identities"), func(ctx context.Context, raw json.RawMessage) (actool.Result, error) {
			var in struct {
				URL       string            `json:"url"`
				Method    string            `json:"method"`
				Body      string            `json:"body"`
				Owner     string            `json:"owner"`
				Identities []authzIdentity  `json:"identities"`
				Timeout   int               `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(raw, &in); err != nil {
				return actool.Errorf("参数解析失败：" + err.Error()), nil
			}
			if strings.TrimSpace(in.URL) == "" {
				return actool.Errorf("url 不能为空"), nil
			}
			if len(in.Identities) < 2 {
				return actool.Errorf("identities 至少给两个身份，只有一个没法比"), nil
			}
			if len(in.Identities) > authzMaxIdentities {
				in.Identities = in.Identities[:authzMaxIdentities]
			}
			timeout := authzDefaultTime
			if in.Timeout > 0 {
				if in.Timeout > 60 {
					in.Timeout = 60
				}
				timeout = time.Duration(in.Timeout) * time.Second
			}
			method := strings.ToUpper(strings.TrimSpace(in.Method))
			if method == "" {
				method = http.MethodGet
			}

			replies := make([]authzReply, 0, len(in.Identities))
			for _, id := range in.Identities {
				replies = append(replies, t.replayAs(ctx, method, in.URL, in.Body, id, timeout))
			}

			base := replies[0]
			if in.Owner != "" {
				for _, r := range replies {
					if strings.EqualFold(r.Name, in.Owner) {
						base = r
						break
					}
				}
			}

			out := &strings.Builder{}
			fmt.Fprintf(out, "对象: %s %s\n基线身份: %s\n\n", method, sanitizeURL(in.URL), base.Name)
			fmt.Fprintf(out, "%-18s %-6s %-9s %-7s %s\n", "身份", "状态", "长度", "耗时", "判定")
			verdicts := map[string]string{}
			for _, r := range replies {
				v := authzVerdict(base, r)
				verdicts[r.Name] = v
				status := fmt.Sprintf("%d", r.Status)
				if r.Err != "" {
					status = "ERR"
				}
				fmt.Fprintf(out, "%-18s %-6s %-9d %-7d %s\n", r.Name, status, r.Length, r.Millis, v)
			}

			findings := authzFindings(base, replies)
			out.WriteString("\n")
			if len(findings) == 0 {
				out.WriteString("未发现身份间的内容差异：每个身份拿到的都是自己的那份，或都被明确拒绝。\n")
			} else {
				for _, f := range findings {
					fmt.Fprintf(out, "[!] %s\n", f)
				}
				out.WriteString("\n结论要落到证据上：把上面这条请求原样写进 evidence（带两个身份的响应体摘要），才算可复现。\n")
			}

			// 响应体样本（截断），供写报告引用
			for _, r := range replies {
				if r.Body == "" {
					continue
				}
				sample := collapseWS(r.Body)
				if len(sample) > 300 {
					sample = sample[:300] + "…"
				}
				fmt.Fprintf(out, "\n--- %s 响应样本 ---\n%s\n", r.Name, sample)
			}
			return actool.Text(strings.TrimRight(out.String(), "\n")), nil
		})
}

func (t *ToolSet) replayAs(ctx context.Context, method, target, body string, id authzIdentity, timeout time.Duration) authzReply {
	r := authzReply{Name: id.Name}
	if r.Name == "" {
		r.Name = "unnamed"
	}
	client, err := t.httpClient(int(timeout.Seconds()), false)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		r.Err = err.Error()
		return r
	}
	for k, v := range id.Headers {
		if strings.EqualFold(k, "host") || strings.EqualFold(k, "content-length") {
			continue
		}
		req.Header.Set(k, v)
	}
	if id.Cookie != "" {
		req.Header.Set("Cookie", id.Cookie)
	}
	if req.Header.Get("User-Agent") == "" {
		req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX)")
	}
	if body != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	start := time.Now()
	resp, err := client.Do(req)
	r.Millis = time.Since(start).Milliseconds()
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, authzMaxBody))
	r.Status = resp.StatusCode
	r.Length = len(raw)
	r.Body = string(raw)
	return r
}

func authzVerdict(base, r authzReply) string {
	switch {
	case r.Err != "":
		return "请求失败"
	case r.denied() && r.Status >= 400:
		return "被拒"
	case !r.ok():
		return "异常"
	case r.Name == base.Name:
		return "基线"
	case authzSimilar(base.Body, r.Body) && authzLenClose(base.Length, r.Length):
		return "同内容"
	case base.denied() && r.ok():
		return "反而放行"
	default:
		return "各自内容"
	}
}

func authzFindings(base authzReply, replies []authzReply) []string {
	var out []string
	for _, r := range replies {
		if r.Name == base.Name || r.Err != "" {
			continue
		}
		switch {
		case r.ok() && base.denied():
			out = append(out, fmt.Sprintf("%s 被拒，但 %s 拿到 %d 字节的 200 响应——垂直/水平越权重点怀疑对象。", base.Name, r.Name, r.Length))
		case r.ok() && base.ok() && authzSimilar(base.Body, r.Body) && authzLenClose(base.Length, r.Length):
			out = append(out, fmt.Sprintf("%s 与 %s 拿到完全一致的内容（相似度 ≥%.0f%%，长度差 ≤%.0f%%）。若这份数据本应只属于其中一个账号，就是水平越权；若是公共页面则忽略本条。", base.Name, r.Name, authzSameRatio*100, authzSameLenDelta*100))
		case r.ok() && base.ok() && !authzSimilar(base.Body, r.Body):
			out = append(out, fmt.Sprintf("%s 与 %s 都是 200 但内容不同——检查其中是否混入了别人的对象字段（同一模板不同数据）。", base.Name, r.Name))
		}
	}
	sort.Strings(out)
	return out
}

func authzLenClose(a, b int) bool {
	if a == 0 && b == 0 {
		return true
	}
	max := a
	if b > max {
		max = b
	}
	if max == 0 {
		return true
	}
	d := a - b
	if d < 0 {
		d = -d
	}
	return float64(d)/float64(max) <= authzSameLenDelta
}

// authzSimilar 先抹掉易变字段（时间戳、csrf 串、uuid、长十六进制），再比：
// token 集合的 Jaccard 处理长页面，字符三元组的 Dice 处理短响应——
// 短页面 token 太少，单靠 Jaccard 会把「同一个卡片、时间不同」判成不同。
func authzSimilar(a, b string) bool {
	na, nb := authzNormalize(a), authzNormalize(b)
	if na == nb {
		return true
	}
	if authzJaccard(authzTokens(na), authzTokens(nb)) >= authzSameRatio {
		return true
	}
	return authzDice(na, nb) >= authzTrigramRatio
}

var (
	reAuthzTS  = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})?`)
	reAuthzUUID = regexp.MustCompile(`(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	reAuthzHex = regexp.MustCompile(`(?i)\b[0-9a-f]{16,}\b`)
	reAuthzNum = regexp.MustCompile(`\b\d{8,}\b`)
)

func authzNormalize(s string) string {
	s = reAuthzTS.ReplaceAllString(s, "<ts>")
	s = reAuthzUUID.ReplaceAllString(s, "<uuid>")
	s = reAuthzHex.ReplaceAllString(s, "<hex>")
	s = reAuthzNum.ReplaceAllString(s, "<num>")
	return collapseWS(s)
}

func authzJaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	inter := 0
	for k := range a {
		if _, ok := b[k]; ok {
			inter++
		}
	}
	union := len(a) + len(b) - inter
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func authzDice(a, b string) float64 {
	ta, tb := authzTrigrams(a), authzTrigrams(b)
	if len(ta) == 0 || len(tb) == 0 {
		if a == b {
			return 1
		}
		return 0
	}
	inter := 0
	for k := range ta {
		if _, ok := tb[k]; ok {
			inter++
		}
	}
	return 2 * float64(inter) / float64(len(ta)+len(tb))
}

func authzTrigrams(s string) map[string]struct{} {
	r := []rune(s)
	out := make(map[string]struct{}, len(r))
	if len(r) < 3 {
		if len(r) > 0 {
			out[string(r)] = struct{}{}
		}
		return out
	}
	for i := 0; i+3 <= len(r); i++ {
		out[string(r[i:i+3])] = struct{}{}
	}
	return out
}

func authzTokens(s string) map[string]struct{} {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\n' || r == '\r' || r == '\t' || r == '<' || r == '>' || r == '"'
	})
	out := make(map[string]struct{}, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if len(f) < 2 {
			continue
		}
		out[f] = struct{}{}
		if len(out) > 4000 {
			break
		}
	}
	return out
}

func collapseWS(s string) string { return strings.Join(strings.Fields(s), " ") }
