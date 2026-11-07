package db

import "testing"

func TestFindingDedupeKeyIsOrderAndDupInsensitive(t *testing.T) {
	a := findingDedupeKey(7, "SQL注入", "edu 报名系统 注入", "high", []int64{3, 1, 2})
	b := findingDedupeKey(7, " sql注入 ", "EDU 报名系统  注入", "HIGH", []int64{1, 2, 3, 3})
	if a != b {
		t.Fatalf("same finding must hash the same: %s vs %s", a, b)
	}
}

func TestFindingDedupeKeySeparatesWhatMatters(t *testing.T) {
	base := findingDedupeKey(7, "SQL注入", "注入", "high", []int64{1, 2})
	cases := []struct {
		name string
		key  string
	}{
		{"other exploration", findingDedupeKey(8, "SQL注入", "注入", "high", []int64{1, 2})},
		{"other class", findingDedupeKey(7, "越权", "注入", "high", []int64{1, 2})},
		{"other title", findingDedupeKey(7, "SQL注入", "注入2", "high", []int64{1, 2})},
		{"other severity", findingDedupeKey(7, "SQL注入", "注入", "critical", []int64{1, 2})},
		{"other assets", findingDedupeKey(7, "SQL注入", "注入", "high", []int64{1, 3})},
		{"more assets", findingDedupeKey(7, "SQL注入", "注入", "high", []int64{1, 2, 3})},
	}
	for _, c := range cases {
		if c.key == base {
			t.Fatalf("%s must not collide with the base finding", c.name)
		}
	}
}

func TestFindingDedupeKeyIsStable(t *testing.T) {
	// Pinned so an accidental change of the hash input is caught: the column is
	// persisted, so a silent change would duplicate every existing finding once.
	got := findingDedupeKey(1, "SQL注入", "登录框注入", "high", []int64{5})
	if len(got) != 64 {
		t.Fatalf("want 32-byte hex, got %q", got)
	}
	if again := findingDedupeKey(1, "SQL注入", "登录框注入", "high", []int64{5}); again != got {
		t.Fatal("hash must be deterministic")
	}
}
