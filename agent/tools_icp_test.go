package agent

import (
	"context"
	"os"
	"strings"
	"testing"
)

const icpSampleICPPage = `<html><body><table>
<tr><td>备案类型</td><td>事业单位</td></tr>
<tr><td>备案主体</td><td>四川商务职业学院</td></tr>
</table>
<table>
<tr><th>子备案号</th><th>备案域名</th><th>更新时间</th></tr>
<tr><td>蜀ICP备18013411号-1</td><td>scsw.edu.cn</td><td>2026-11-05</td></tr>
<tr><td>蜀ICP备18013411号-12</td><td>scswcw.cn</td><td>2026-11-05</td></tr>
</table>
<table><tr><td>weiyubrand.cn</td></tr><tr><td>thinkshub.com</td></tr></table>
</body></html>`

const icpSampleSubjectPage = `<html><body><table>
<tr><th>备案号</th><th>备案主体</th><th>备案域名</th><th>更新时间</th></tr>
<tr><td>蜀ICP备18013411号-1</td><td>四川商务职业学院</td><td>scsw.edu.cn</td><td>2026-11-05</td></tr>
<tr><td>蜀ICP备08000151号-1</td><td>四川商务职业学院</td><td>scsw.net.cn</td><td>2017-09-03</td></tr>
</table></body></html>`

func TestICPRowsParsesSubRecords(t *testing.T) {
	rows := icpRows(icpSampleICPPage)
	if len(rows) < 4 {
		t.Fatalf("rows = %d, want at least 4", len(rows))
	}
	lines := icpDomainRows(rows, 50)
	if len(lines) != 2 {
		t.Fatalf("domain rows = %v, want 2", lines)
	}
	if !strings.Contains(lines[0], "蜀ICP备18013411号-1") || !strings.Contains(lines[0], "scsw.edu.cn") {
		t.Fatalf("first line = %q", lines[0])
	}
}

func TestICPSubjectRowsKeepHistory(t *testing.T) {
	lines := icpDomainRows(icpRows(icpSampleSubjectPage), 50)
	if len(lines) != 2 {
		t.Fatalf("lines = %v", lines)
	}
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "scsw.net.cn") || !strings.Contains(joined, "2017-09-03") {
		t.Fatalf("history row lost: %s", joined)
	}
}

func TestICPKeyValues(t *testing.T) {
	kv := icpKV(icpRows(icpSampleICPPage))
	if kv["备案类型"] != "事业单位" {
		t.Fatalf("备案类型 = %q", kv["备案类型"])
	}
	if kv["备案主体"] != "四川商务职业学院" {
		t.Fatalf("备案主体 = %q", kv["备案主体"])
	}
}

func TestICPRejectsNoiseRows(t *testing.T) {
	rows := icpRows(`<table><tr><td>数据更新时间：2026-11-05</td></tr><tr><td>网站名称</td><td>某站</td></tr></table>`)
	if got := icpDomainRows(rows, 10); len(got) != 0 {
		t.Fatalf("noise became domain rows: %v", got)
	}
}

// TestICPLiveQuery hits the real source; run with ARTEX_LIVE_RECON=1.
func TestICPLiveQuery(t *testing.T) {
	if os.Getenv("ARTEX_LIVE_RECON") != "1" {
		t.Skip("set ARTEX_LIVE_RECON=1 to hit icplishi.com")
	}
	ctx := context.Background()
	out := icpQuery(ctx, "domain", "scsw.edu.cn", 20)
	if !strings.Contains(out, "ICP备") {
		t.Fatalf("domain query found no 备案号:\n%s", out)
	}
	t.Logf("domain mode:\n%s", out)

	out = icpQuery(ctx, "icp", "蜀ICP备18013411号", 20)
	if !strings.Contains(out, "scsw.edu.cn") {
		t.Fatalf("icp reverse found no domain:\n%s", out)
	}
	t.Logf("icp mode:\n%s", out)

	out = icpQuery(ctx, "subject", "四川商务职业学院", 20)
	if !strings.Contains(out, "scsw.net.cn") {
		t.Fatalf("subject mode missed historical 备案:\n%s", out)
	}
	t.Logf("subject mode:\n%s", out)
}
