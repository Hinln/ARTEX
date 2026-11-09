package intercept

import "testing"

// 战果内网判定靠「这个地址是从哪一次执行输出里看来的」：只有真的跑出过它才算。
func TestPrivateHostSeenInLedger(t *testing.T) {
	const sess = "901"
	RecordExecution(sess, "Bash", "curl -sS http://203.0.113.7:6379/info",
		"redis_version:6.2.6\n# Server\n", false)
	if PrivateHostSeenInLedger(sess, "10.1.2.3") {
		t.Fatal("an address nobody printed must not count as a pivot")
	}
	RecordExecution(sess, "Bash", "curl -sS 'http://203.0.113.7:6379/?cmd=slaveof'",
		"[redis] 10.1.2.3:3306 reachable, 10.1.2.9:22 open\n", false)
	if !PrivateHostSeenInLedger(sess, "10.1.2.3") {
		t.Fatal("an address printed by a real command is a pivot")
	}
	if PrivateHostSeenInLedger("902", "10.1.2.3") {
		t.Fatal("pivot evidence must not leak across sessions")
	}
}
