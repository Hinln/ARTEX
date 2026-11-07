package agent

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"strings"
	"testing"

	actool "github.com/Autumn-27/norma/tool"
)

func liveToolText(t *testing.T, tool actool.CoreTool, args map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	res, err := tool.Call(context.Background(), raw, nil)
	if err != nil {
		t.Fatalf("%s call: %v", tool.Name(), err)
	}
	var sb strings.Builder
	for _, b := range res.Content {
		sb.WriteString(b.Text)
	}
	return sb.String()
}

// TestOOBLiveRoundTrip 只验证通道本身能用：开监听 → 自己解析一次子域 → 收回连。
// 需要外网，默认跳过：ARTEX_LIVE_RECON=1 go test ./agent -run TestOOBLive -v
func TestOOBLiveRoundTrip(t *testing.T) {
	if os.Getenv("ARTEX_LIVE_RECON") != "1" {
		t.Skip("set ARTEX_LIVE_RECON=1 to hit dnslog.cn")
	}
	ts := &ToolSet{}
	arm := liveToolText(t, ts.oobArm(), map[string]any{"label": "unit-test"})
	if !strings.Contains(arm, "域名:") {
		t.Fatalf("arm failed:\n%s", arm)
	}
	var token, domain string
	for _, line := range strings.Split(arm, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "token:") {
			token = strings.TrimSpace(strings.TrimPrefix(line, "token:"))
		}
		if strings.HasPrefix(line, "域名:") {
			domain = strings.TrimSpace(strings.TrimPrefix(line, "域名:"))
		}
	}
	if token == "" || domain == "" {
		t.Fatalf("cannot parse arm output:\n%s", arm)
	}
	host := token + "." + domain
	if _, err := net.LookupHost(host); err != nil {
		t.Logf("lookup %s: %v (记录可能仍然落在服务端)", host, err)
	}
	check := liveToolText(t, ts.oobCheck(), map[string]any{"token": token, "wait_seconds": 20})
	t.Logf("check output:\n%s", check)
	if strings.Contains(check, "暂无回连") {
		t.Fatalf("no callback recorded:\n%s", check)
	}
	_ = context.Background()
}
