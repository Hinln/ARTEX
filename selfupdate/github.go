package selfupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// Repo 是发布源。写死而不是做成配置项：更新源可配等于给任何能改配置的人一条
// 远程代码执行通道，对一个渗透测试平台来说这个口子开不得。
const Repo = "Hinln/ARTEX"

// GitHubTokenEnv 只由后端进程读取，不通过前端或配置 API 传递令牌。
const GitHubTokenEnv = "ARTEX_UPDATE_GITHUB_TOKEN"

// latestURL 是 GitHub 的"最新正式版"接口。它会自动跳过 prerelease 和 draft。
const latestURL = "https://api.github.com/repos/" + Repo + "/releases/latest"

// allowedHosts 限定升级链路能访问的域名。配合下面的 checkRedirect，
// 任何一跳被重定向到名单外的主机都会直接失败——这是防止 DNS 污染 / 中间人
// 把二进制换掉的第一道闸门，第二道是 SHA256SUMS 比对。
var allowedHosts = map[string]bool{
	"api.github.com":                       true,
	"github.com":                           true,
	"objects.githubusercontent.com":        true, // release 资产实际落地的对象存储
	"release-assets.githubusercontent.com": true,
	"raw.githubusercontent.com":            true,
}

// Release 是 GitHub Release 里我们关心的字段。
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Assets      []Asset   `json:"assets"`
}

// Asset 是 Release 上挂的一个文件。
type Asset struct {
	Name   string `json:"name"`
	URL    string `json:"browser_download_url"`
	APIURL string `json:"url"`
	Size   int64  `json:"size"`
}

// 私有仓库的附件需通过 Release Asset API 携带认证下载。
func (a Asset) downloadURL() string {
	if a.APIURL != "" {
		return a.APIURL
	}
	return a.URL
}

// setUpdateAuth 仅向固定发布仓库的 HTTPS API 发送令牌。附件重定向到
// GitHub 对象存储时使用签名 URL，不得把 Authorization 一并转发。
func setUpdateAuth(req *http.Request) {
	req.Header.Del("Authorization")
	u := req.URL
	if u.Scheme != "https" || !strings.EqualFold(u.Hostname(), "api.github.com") ||
		(u.Port() != "" && u.Port() != "443") || u.User != nil ||
		!strings.HasPrefix(strings.ToLower(u.EscapedPath()), strings.ToLower("/repos/"+Repo+"/releases/")) {
		return
	}
	if token := strings.TrimSpace(os.Getenv(GitHubTokenEnv)); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
}

// NewClient 构造一个只认 GitHub 域名的 HTTP 客户端。proxy 为空则直连。
//
// 刻意不复用默认 Transport：升级链路必须强制走 TLS 且校验证书，不能被别处
// 设置的 InsecureSkipVerify 之类影响到。
func NewClient(proxy string) *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	if p := strings.TrimSpace(proxy); p != "" {
		if pu, err := url.Parse(p); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Minute, // 下载整包，不能按请求级超时卡死
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("重定向次数过多")
			}
			if err := checkURL(req.URL); err != nil {
				return err
			}
			setUpdateAuth(req)
			return nil
		},
	}
}

// checkURL 强制 https + 域名白名单。
func checkURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("拒绝非 HTTPS 地址: %s", u.Scheme+"://"+u.Host)
	}
	if !allowedHosts[strings.ToLower(u.Hostname())] {
		return fmt.Errorf("拒绝非 GitHub 域名: %s", u.Hostname())
	}
	return nil
}

// FetchLatest 查询最新正式版。
func FetchLatest(ctx context.Context, c *http.Client) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, latestURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "artex-selfupdate")
	setUpdateAuth(req)

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("访问 GitHub 失败（可在系统设置里配置全局代理）: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusUnauthorized:
		return nil, fmt.Errorf("GitHub 认证失败，请检查后端环境变量 %s", GitHubTokenEnv)
	case resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("GitHub 拒绝访问：请检查令牌的仓库 Contents 读取权限或 API 限额")
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub 接口限流，请稍后再试")
	case resp.StatusCode == http.StatusNotFound:
		return nil, fmt.Errorf("仓库 %s 不可访问或尚未发布正式版本；私有仓库请在后端设置 %s（需 Contents 读取权限）", Repo, GitHubTokenEnv)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("GitHub 返回 %d", resp.StatusCode)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("解析 Release 失败: %w", err)
	}
	if strings.TrimSpace(rel.TagName) == "" {
		return nil, fmt.Errorf("Release 缺少 tag")
	}
	return &rel, nil
}

// AssetName 返回当前平台对应的发布包名，与 build.sh 的 package_binary 保持一致：
// artex-<版本>-<os>-<arch>.zip（版本号不带 v 前缀）。
func AssetName(tag, goos, goarch string) string {
	return fmt.Sprintf("artex-%s-%s-%s.zip", strings.TrimPrefix(tag, "v"), goos, goarch)
}

// FindAsset 在 Release 里按名字找资产。
func (r *Release) FindAsset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if strings.EqualFold(a.Name, name) {
			return a, true
		}
	}
	return Asset{}, false
}
