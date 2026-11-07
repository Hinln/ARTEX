package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// 备案（ICP）反查：主办单位 -> 全部子备案号 -> 全部域名。
//
// 这是扩攻击面最便宜的一条路：一个主体名下通常挂着十几个域，主站往往不是最弱的那个。
// 直接打工信部（beian.miit.gov.cn）要过 JS 挑战 + 人工校验码，机器走不通；
// 第三方接口（vvhan / uomg / oioweb / bearktrue / beianx / chinaz / aizhan / bugscaner）
// 这两年陆续下线或改成 XHR 壳。icplishi.com 是目前唯一还稳定的三向查询源：
//
//	/                <域名>/          域名 -> 备案号/主体/网站名
//	/                <备案号>/        备案号 -> 该主体全部子备案号与域名
//	/                site/<主体名>/   主体名 -> 历史全部备案（含旧号、旧域）
//
// 兜底走 L1：直接抓目标首页 footer 里的备案号原文，自证不依赖第三方。

const (
	icpBaseURL  = "https://icplishi.com"
	icpMaxRows  = 200
	icpTimeout  = 30 * time.Second
	icpMaxBody  = 4 << 20
	icpUAString = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
)

var (
	// 省份简称 + ICP备 + 数字 + 号，可选 -N 子号。
	reICPNo = regexp.MustCompile(`([京津冀晋蒙辽吉黑沪苏浙皖闽赣鲁豫鄂湘粤桂琼渝川贵云藏陕甘青宁新台蜀黔滇港澳门维青][A-Z]?ICP备\d+号(?:-\d+)?)`)
	reTag   = regexp.MustCompile(`(?s)<[^>]+>`)
	reTR    = regexp.MustCompile(`(?is)<tr[^>]*>(.*?)</tr>`)
	reTD    = regexp.MustCompile(`(?is)<t[dh][^>]*>(.*?)</t[dh]>`)
	reWS    = regexp.MustCompile(`[\s\x{00a0}]+`)
)

func (t *ToolSet) icpLookup() actool.CoreTool {
	return readTool("icp_lookup", "ICP 备案反查：给域名/备案号/主办单位任一，返回备案号、主办单位、该主体名下全部子备案号与域名（扩攻击面），或抓目标首页 footer 里的备案号原文自证。只查 icplishi.com 与目标首页，不做扫描。",
		obj(map[string]any{
			"query": str("域名（example.com）、备案号（蜀ICP备18013411号）或主办单位名称（四川商务职业学院）"),
			"mode":  icpEnum("查询方向：auto 自动判断（默认）、domain 按域名、icp 按备案号反查、subject 按主办单位反查、footer 抓目标首页页脚备案号", []string{"auto", "domain", "icp", "subject", "footer"}),
			"limit": map[string]any{"type": "integer", "description": "最多返回的域名/备案行数，默认 50，上限 200"},
		}, "query"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				Query string `json:"query"`
				Mode  string `json:"mode"`
				Limit int    `json:"limit"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(fmt.Sprintf("参数解析失败: %v", err)), nil
			}
			q := strings.TrimSpace(a.Query)
			if q == "" {
				return actool.Errorf("icp_lookup 需要 query：域名、备案号或主办单位名称"), nil
			}
			limit := a.Limit
			if limit <= 0 || limit > icpMaxRows {
				limit = 50
			}
			mode := strings.ToLower(strings.TrimSpace(a.Mode))
			switch mode {
			case "", "auto":
				switch {
				case strings.Contains(q, "ICP备"):
					mode = "icp"
				case strings.ContainsAny(q, "./") || strings.HasPrefix(q, "http"):
					mode = "domain"
				default:
					mode = "subject"
				}
			case "domain", "icp", "subject", "footer":
			default:
				return actool.Errorf("mode 只能是 auto/domain/icp/subject/footer"), nil
			}
			ctx, cancel := context.WithTimeout(ctx, icpTimeout)
			defer cancel()
			if mode == "footer" {
				return actool.Text(icpFooterScan(ctx, t, q)), nil
			}
			return actool.Text(icpQuery(ctx, mode, q, limit)), nil
		})
}

// icpQuery hits icplishi.com in one of the three directions and renders the answer.
func icpQuery(ctx context.Context, mode, q string, limit int) string {
	target := ""
	switch mode {
	case "domain":
		host := normalizeDomain(q)
		if host == "" {
			return "域名解析失败：给 example.com 这种形式。"
		}
		target = icpBaseURL + "/" + url.PathEscape(host) + "/"
	case "icp":
		no := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(q), " ", ""))
		if m := reICPNo.FindString(no); m != "" {
			no = m
		}
		target = icpBaseURL + "/" + url.PathEscape(no) + "/"
	case "subject":
		target = icpBaseURL + "/site/" + url.PathEscape(strings.TrimSpace(q)) + "/"
	}
	code, body, err := reconGet(ctx, icpClient(), target, map[string]string{"User-Agent": icpUAString})
	if err != nil {
		return fmt.Sprintf("备案查询失败（icplishi.com）：%v\n来源: %s\n退路：抓目标首页页脚自证（mode=footer），或人工在 beian.miit.gov.cn 按备案号核验。", err, target)
	}
	if code != http.StatusOK {
		return fmt.Sprintf("备案查询失败：HTTP %d\n来源: %s\n退路：换 mode=footer 抓页脚，或人工核验。", code, target)
	}
	html := string(body)
	rows := icpRows(html)
	var b strings.Builder
	fmt.Fprintf(&b, "来源: %s\n", target)
	switch mode {
	case "domain":
		pairs := icpKV(rows)
		if len(pairs) == 0 && len(rows) == 0 {
			b.WriteString("无备案记录（该域名可能未备案、或数据未收录）。\n")
			break
		}
		for _, k := range []string{"网站名称", "备案类型", "备案主体", "备案号", "审核通过日期", "备案时间"} {
			if v, ok := pairs[k]; ok && v != "" {
				fmt.Fprintf(&b, "%s: %s\n", k, v)
			}
		}
		if no := pairs["备案号"]; no != "" {
			fmt.Fprintf(&b, "建议下一步: icp_lookup(mode=icp, query=%q) 反查同主体全部域名\n", no)
		}
	case "icp":
		pairs := icpKV(rows)
		if v := pairs["备案类型"]; v != "" {
			fmt.Fprintf(&b, "备案类型: %s\n", v)
		}
		if v := pairs["备案主体"]; v != "" {
			fmt.Fprintf(&b, "备案主体: %s\n", v)
		}
		lines := icpDomainRows(rows, limit)
		fmt.Fprintf(&b, "同主体子备案: %d 条\n", len(lines))
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		if len(lines) == 0 {
			b.WriteString("没有子备案行：可能该备案号只有单域名，或页面结构变动。\n")
		}
		if subj := pairs["备案主体"]; subj != "" {
			fmt.Fprintf(&b, "建议下一步: icp_lookup(mode=subject, query=%q) 查该主体历史全部备案（含旧号旧域）\n", subj)
		}
	case "subject":
		lines := icpDomainRows(rows, limit)
		fmt.Fprintf(&b, "该主体备案记录: %d 条\n", len(lines))
		for _, l := range lines {
			b.WriteString(l + "\n")
		}
		if len(lines) == 0 {
			b.WriteString("无记录：换主体全名再试，或用域名走 mode=domain。\n")
		}
	}
	return b.String()
}

// icpFooterScan is the L1 fallback: read the备案号 off the site's own footer.
func icpFooterScan(ctx context.Context, t *ToolSet, raw string) string {
	u := strings.TrimSpace(raw)
	if !strings.HasPrefix(u, "http") {
		host := normalizeDomain(u)
		if host == "" {
			return "footer 模式需要域名或 URL。"
		}
		u = "https://" + host + "/"
	}
	client := icpClient()
	if tc := t.reconTargetClient(); tc != nil {
		client = tc
	}
	code, body, err := reconGet(ctx, client, u, map[string]string{"User-Agent": icpUAString})
	if err != nil {
		return fmt.Sprintf("抓取失败：%v\n来源: %s", err, u)
	}
	text := reTag.ReplaceAllString(string(body), " ")
	found := reICPNo.FindAllString(text, 20)
	if len(found) == 0 {
		return fmt.Sprintf("HTTP %d，页脚未出现备案号（可能被 WAF 拦、页脚改版，或确实未备案）。\n来源: %s", code, u)
	}
	seen := map[string]bool{}
	var b strings.Builder
	fmt.Fprintf(&b, "来源: %s（HTTP %d，页面原文）\n", u, code)
	for _, f := range found {
		if seen[f] {
			continue
		}
		seen[f] = true
		fmt.Fprintf(&b, "备案号: %s\n", f)
	}
	fmt.Fprintf(&b, "建议下一步: icp_lookup(mode=icp, query=%q) 反查同主体域名\n", found[0])
	return b.String()
}

func icpClient() *http.Client {
	// icplishi.com 在境内可直连；国内出口需要代理时由 http_proxy 环境变量接管。
	return reconClient(0)
}

func icpEnum(desc string, vals []string) map[string]any {
	return map[string]any{"type": "string", "description": desc, "enum": vals}
}

// icpRows splits the page into per-<tr> cell lists (tag-stripped, whitespace-collapsed).
func icpRows(html string) [][]string {
	// 只保留表格区域，页脚那堆"最新备案"噪音也在这里面，靠内容过滤区分。
	out := make([][]string, 0, 64)
	for _, trm := range reTR.FindAllStringSubmatch(html, -1) {
		cells := make([]string, 0, 4)
		for _, tdm := range reTD.FindAllStringSubmatch(trm[1], -1) {
			c := reTag.ReplaceAllString(tdm[1], " ")
			c = reWS.ReplaceAllString(c, " ")
			c = strings.TrimSpace(c)
			cells = append(cells, c)
		}
		if len(cells) == 0 {
			continue
		}
		empty := true
		for _, c := range cells {
			if c != "" {
				empty = false
				break
			}
		}
		if empty {
			continue
		}
		out = append(out, cells)
	}
	return out
}

// icpKV turns a two-cell layout (label, value) into a map for the domain page.
func icpKV(rows [][]string) map[string]string {
	want := map[string]bool{
		"网站名称": true, "备案类型": true, "备案主体": true, "备案号": true,
		"审核通过日期": true, "备案时间": true, "主办单位性质": true, "主办单位": true,
	}
	kv := map[string]string{}
	for _, row := range rows {
		for i := 0; i+1 < len(row); i++ {
			k := strings.TrimSpace(row[i])
			if !want[k] {
				continue
			}
			v := strings.TrimSpace(row[i+1])
			if v == "" || want[v] {
				continue
			}
			if _, ok := kv[k]; !ok {
				kv[k] = v
			}
		}
	}
	return kv
}

// icpDomainRows keeps rows that look like "备案号 | 域名 | 更新时间".
func icpDomainRows(rows [][]string, limit int) []string {
	out := make([]string, 0, 32)
	seen := map[string]bool{}
	for _, row := range rows {
		var no, host, updated string
		for _, c := range row {
			if no == "" && reICPNo.MatchString(c) {
				no = reICPNo.FindString(c)
				continue
			}
			if host == "" && icpHostLike(c) {
				host = c
				continue
			}
			if updated == "" && icpDateLike(c) {
				updated = c
			}
		}
		if no == "" || host == "" {
			continue
		}
		line := no + " | " + host
		if updated != "" {
			line += " | " + updated
		}
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func icpHostLike(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " |<>\t") {
		return false
	}
	if !strings.Contains(s, ".") {
		return false
	}
	if strings.Contains(s, "ICP备") {
		return false
	}
	// 只认域名字符，挡掉"数据更新时间"这类中文句子
	for _, r := range s {
		if r > 127 && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

func icpDateLike(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) < 8 || len(s) > 10 {
		return false
	}
	return s[4] == '-' && s[7] == '-' && strings.Count(s, "-") == 2
}
