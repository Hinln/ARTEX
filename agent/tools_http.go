package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// httpMaxBody bounds what one native request may pull back. A worker reading a
// 40MB JS bundle burns its context on bytes no finding needs.
const httpMaxBody = 512 * 1024

// httpDefaultTimeout keeps one request from holding a worker slot.
const httpDefaultTimeout = 15

// hopHeaders are stripped from the response echo: they describe the connection,
// not the target, and copying them back into a report is noise.
var hopHeaders = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true,
	"Proxy-Authorization": true, "Te": true, "Trailer": true,
	"Transfer-Encoding": true, "Upgrade": true,
}

// SetEgress binds the recording proxy this task's traffic must pass through, so
// a native request is captured the same way a curl issued from Bash is. Without
// it the tool still works, but the request never reaches the evidence store.
func (t *ToolSet) SetEgress(proxyAddr, caCert string) {
	t.egressProxy, t.egressCA = proxyAddr, caCert
}

// httpRequest is a native HTTP client for workers. Bash+curl remains available;
// this exists so the common case (one request, read the response) does not need
// a shell, and so the request lands in the recording proxy by default.
func (t *ToolSet) httpRequest() actool.CoreTool {
	return writeTool("http_request", "发起一次 HTTP 请求并取回状态行、响应头与正文（默认不跟随跳转、正文截断 512KB）。目标请求经录制代理,证据自动入库。用于验证单点结论;批量/多目标枚举用脚本。",
		obj(map[string]any{
			"url":    str("完整 URL,含 http:// 或 https://"),
			"method": str("GET/POST/PUT/DELETE/PATCH/HEAD/OPTIONS,默认 GET"),
			"headers": map[string]any{
				"type":        "object",
				"description": "请求头键值对",
				"additionalProperties": map[string]any{"type": "string"},
			},
			"body":             str("请求正文(GET/HEAD 忽略)"),
			"follow_redirects": map[string]any{"type": "boolean", "description": "默认 false:不跟随,保留 3xx 与 Location"},
			"timeout_seconds":  map[string]any{"type": "integer", "description": "默认 15,上限 60"},
		}, "url"), func(ctx context.Context, in json.RawMessage) (actool.Result, error) {
			var a struct {
				URL             string            `json:"url"`
				Method          string            `json:"method"`
				Headers         map[string]string `json:"headers"`
				Body            string            `json:"body"`
				FollowRedirects bool              `json:"follow_redirects"`
				TimeoutSeconds  int               `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(in, &a); err != nil {
				return actool.Errorf(err.Error()), nil
			}
			raw := strings.TrimSpace(a.URL)
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" {
				return actool.Errorf(fmt.Sprintf("url 无效: %s", raw)), nil
			}
			switch strings.ToLower(u.Scheme) {
			case "http", "https":
			default:
				return actool.Errorf(fmt.Sprintf("只支持 http/https,收到 %q", u.Scheme)), nil
			}
			method := strings.ToUpper(strings.TrimSpace(a.Method))
			if method == "" {
				method = http.MethodGet
			}
			timeout := a.TimeoutSeconds
			if timeout <= 0 {
				timeout = httpDefaultTimeout
			}
			if timeout > 60 {
				timeout = 60
			}
			client, err := t.httpClient(timeout, a.FollowRedirects)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			var body io.Reader
			if a.Body != "" && method != http.MethodGet && method != http.MethodHead {
				body = strings.NewReader(a.Body)
			}
			reqCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(reqCtx, method, raw, body)
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			for k, v := range a.Headers {
				req.Header.Set(k, v)
			}
			if req.Header.Get("User-Agent") == "" {
				req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX)")
			}
			start := time.Now()
			resp, err := client.Do(req)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("请求失败: %v", err)), nil
			}
			defer resp.Body.Close()
			limited := io.LimitReader(resp.Body, httpMaxBody+1)
			rawBody, err := io.ReadAll(limited)
			if err != nil {
				return actool.Errorf(fmt.Sprintf("读取响应失败: %v", err)), nil
			}
			truncated := len(rawBody) > httpMaxBody
			if truncated {
				rawBody = rawBody[:httpMaxBody]
			}
			var out bytes.Buffer
			fmt.Fprintf(&out, "%s %s\n", resp.Proto, resp.Status)
			keys := make([]string, 0, len(resp.Header))
			for k := range resp.Header {
				if hopHeaders[http.CanonicalHeaderKey(k)] {
					continue
				}
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(&out, "%s: %s\n", k, strings.Join(resp.Header[k], ", "))
			}
			fmt.Fprintf(&out, "\n%s", string(rawBody))
			if truncated {
				fmt.Fprintf(&out, "\n\n[正文已截断: >%d 字节]", httpMaxBody)
			}
			fmt.Fprintf(&out, "\n\n[耗时 %dms]", time.Since(start).Milliseconds())
			return actool.Text(out.String()), nil
		})
}

// httpClient builds the client used by http_request. When a recording proxy is
// bound, the request goes through it and the proxy's CA is trusted — that is the
// difference between a verified finding with traffic evidence and one without.
func (t *ToolSet) httpClient(timeout int, follow bool) (*http.Client, error) {
	tr := &http.Transport{
		TLSClientConfig:     &tls.Config{InsecureSkipVerify: true},
		DisableKeepAlives:   true,
		MaxIdleConnsPerHost: 1,
	}
	if t.egressProxy != "" {
		pu, err := url.Parse(t.egressProxy)
		if err != nil {
			return nil, fmt.Errorf("录制代理地址无效: %v", err)
		}
		tr.Proxy = http.ProxyURL(pu)
		if t.egressCA != "" {
			cfg, err := proxyCAPool(t.egressCA)
			if err == nil {
				tr.TLSClientConfig = &tls.Config{RootCAs: cfg}
			}
		}
	} else if env := strings.TrimSpace(os.Getenv("ARTEX_HTTP_PROXY")); env != "" {
		if pu, err := url.Parse(env); err == nil {
			tr.Proxy = http.ProxyURL(pu)
		}
	}
	client := &http.Client{Transport: tr, Timeout: time.Duration(timeout) * time.Second}
	if !follow {
		client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	}
	return client, nil
}

func proxyCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("代理 CA 无法解析: %s", path)
	}
	return pool, nil
}
