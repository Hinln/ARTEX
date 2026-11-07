package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	actool "github.com/Autumn-27/norma/tool"
)

// Live probes against public recon APIs. Skipped unless ARTEX_LIVE_RECON=1 —
// they need egress and they hammer third parties, so they never run in CI.
func TestLiveReconTools(t *testing.T) {
	if os.Getenv("ARTEX_LIVE_RECON") != "1" {
		t.Skip("set ARTEX_LIVE_RECON=1 to hit public recon APIs")
	}
	ts := &ToolSet{}
	cases := []struct {
		name string
		tool actool.CoreTool
		in   string
		want string
	}{
		{"cert_subdomains", ts.certSubdomains(), `{"domain":"example.com","limit":5}`, ""},
		{"wayback_urls", ts.waybackURLs(), `{"domain":"example.com","limit":5}`, ""},
		{"rdap_lookup", ts.rdapLookup(), `{"target":"example.com"}`, ""},
		{"reverse_ip", ts.reverseIP(), `{"ip":"8.8.8.8"}`, ""},
		{"cdn_detect", ts.cdnDetect(), `{"host":"www.cloudflare.com"}`, ""},
		{"favicon_hash", ts.faviconHash(), `{"url":"https://www.baidu.com"}`, "icon_hash"},
		{"subdomain_verify", ts.subdomainVerify(), `{"names":["www.example.com","nope-not-real.example.com"]}`, ""},
		{"fofa_search_no_key", ts.fofaSearch(), `{"query":"domain=\"example.com\""}`, "FOFA 未配置"},
	}
	for _, c := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
		res, err := c.tool.Call(ctx, json.RawMessage(c.in), nil)
		cancel()
		text := fmt.Sprint(res)
		if err != nil {
			t.Logf("[%s] transport err: %v", c.name, err)
			continue
		}
		if c.want != "" && !strings.Contains(text, c.want) {
			t.Errorf("[%s] 期望包含 %q，实际: %.300s", c.name, c.want, text)
			continue
		}
		t.Logf("[%s] %.400s", c.name, text)
	}
}
