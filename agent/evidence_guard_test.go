package agent

import (
	"strings"
	"testing"

	"github.com/Autumn-27/artex/intercept"
)

func TestCalibrateInternalIPIsInfoOnly(t *testing.T) {
	got, changed, note := CalibrateSeverity("内网IP泄露", "10.0.0.5:8080 内网地址", "high")
	if got != "low" {
		t.Fatalf("internal IP should cap at low, got %s (%s)", got, note)
	}
	if !changed {
		t.Fatalf("downgrade not reported")
	}
	if !InfoOnlyClass("内网IP泄露") || !InfoOnlyClass("端口暴露") || !InfoOnlyClass("版本号泄露") {
		t.Fatalf("info-only classes misclassified")
	}
	if InfoOnlyClass("SQL注入") || InfoOnlyClass("命令执行") {
		t.Fatalf("real bug classes flagged as info-only")
	}
}

func TestCalibrateRCEWithoutExecutionEvidence(t *testing.T) {
	ev := "GET /a HTTP/1.1\nHTTP/1.1 200 OK\nContent-Type: text/html"
	got, changed, note := CalibrateSeverity("命令执行", ev, "critical")
	if got != "medium" {
		t.Fatalf("RCE claim on a plain 200 should fall to medium, got %s (%s)", got, note)
	}
	if !changed || !strings.Contains(note, "HTTP") {
		t.Fatalf("note should explain the ceiling: %s", note)
	}
}

func TestCalibrateRCEWithRootEchoStaysCritical(t *testing.T) {
	ev := "$ id\nuid=0(root) gid=0(root) groups=0(root)"
	got, changed, _ := CalibrateSeverity("命令执行", ev, "critical")
	if got != "critical" || changed {
		t.Fatalf("root echo should hold critical, got %s changed=%v", got, changed)
	}
}

func TestCalibrateSQLiErrorHoldsHigh(t *testing.T) {
	ev := "id=1'\nHTTP/1.1 500\nYou have an error in your SQL syntax; check the manual"
	if got, changed, _ := CalibrateSeverity("SQL注入", ev, "critical"); got != "high" {
		t.Fatalf("SQLi with error fingerprint caps at high, got %s changed=%v", got, changed)
	}
}

func TestVerifyEvidenceRejectsHedging(t *testing.T) {
	ok, why := VerifyEvidence("s1", "预计输出：root:x:0:0:root:/root:/bin/bash\n假设这样可以读文件")
	if ok {
		t.Fatalf("hedged evidence accepted")
	}
	if !strings.Contains(why, "推测") {
		t.Fatalf("reason should call out guessing: %s", why)
	}
}

func TestVerifyEvidenceMatchesLedger(t *testing.T) {
	real := "uid=0(root) gid=0(root) groups=0(root)"
	intercept.RecordExecution("sess-verify", "Bash", "id", real, false)

	if ok, _ := VerifyEvidence("sess-verify", "id\n"+real); !ok {
		t.Fatalf("real output rejected")
	}
	fake := "uid=0(root) gid=0(root) groups=0(root) from-imagination-marker-9f3a"
	if ok, why := VerifyEvidence("sess-verify", fake); ok {
		t.Fatalf("fabricated output accepted: %s", why)
	}
}

func TestInfoOnlyRejectsBeforeRecording(t *testing.T) {
	if !InfoOnlyClass("内网IP泄露") {
		t.Fatal("expected info-only")
	}
	if InfoOnlyClass("越权访问") {
		t.Fatal("authz bypass must stay reportable")
	}
}
