package server

// 信息搜集（FOFA key 池）接口的测试。
//
// 重点不是 HTTP 层，而是三条真正会出错的业务规则：
//   1. 读接口必须脱敏——明文 key 不能回显给前端。
//   2. 保存时留空 = 保持原 key，否则前端每改一次顺序就把所有 key 清空。
//   3. 池上限收敛：max_attempts 不能超过实际 key 数。

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaskKeyNeverLeaksPlaintext(t *testing.T) {
	full := "7bb4c7350f79614f4487fd90da7ec612"
	m := maskKey(full)
	if strings.Contains(m, full) {
		t.Fatalf("masked key contains plaintext: %q", m)
	}
	if !strings.HasPrefix(m, "7bb4") || !strings.HasSuffix(m, "c612") {
		t.Fatalf("masked key should keep head/tail for recognition: %q", m)
	}
	// 短 key 不能因为掩码反而暴露全部内容。
	if got := maskKey("abc"); got != "***" {
		t.Fatalf("short key must fully mask, got %q", got)
	}
	if got := maskKey(""); got != "***" {
		t.Fatalf("empty key must mask to ***, got %q", got)
	}
}

func TestSplitKeysTrimsAndDedupes(t *testing.T) {
	got := splitKeys(" a , b ,, a , c ")
	want := []string{"a", "b", "c"}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order must be preserved: got %v want %v", got, want)
		}
	}
	if len(splitKeys("")) != 0 {
		t.Fatal("empty string must yield no keys")
	}
}

func TestMergeKeysKeepsUnchangedEntries(t *testing.T) {
	old := []string{"k1", "k2", "k3"}
	// 前端只填了第二项的新值，其余留空 = 保持。
	in := []fofaKeyEntry{
		{Masked: "***", Value: ""},
		{Masked: "***", Value: "k2new"},
		{Masked: "***", Value: ""},
	}
	got := mergeKeys(old, in)
	if len(got) != 3 || got[0] != "k1" || got[1] != "k2new" || got[2] != "k3" {
		t.Fatalf("positional merge failed: %v", got)
	}
}

func TestMergeKeysAddsAndRemoves(t *testing.T) {
	old := []string{"k1", "k2"}
	// 删除第二项（不在 in 里），并追加一项。
	in := []fofaKeyEntry{
		{Masked: "***", Value: ""},
		{Masked: "", Value: "k3"},
	}
	got := mergeKeys(old, in)
	if len(got) != 2 || got[0] != "k1" || got[1] != "k3" {
		t.Fatalf("add/remove failed: %v", got)
	}
	// 只有空白的新行必须被跳过，而不是变成空 key。
	if got := mergeKeys(old, []fofaKeyEntry{{Value: "  "}}); len(got) != 1 || got[0] != "k1" {
		t.Fatalf("blank new row must be ignored: %v", got)
	}
}

func TestMergeKeysDedupesAcrossPositions(t *testing.T) {
	old := []string{"k1"}
	in := []fofaKeyEntry{{Value: "k1"}, {Value: "k1"}}
	if got := mergeKeys(old, in); len(got) != 1 {
		t.Fatalf("duplicates must collapse: %v", got)
	}
}

// 配置结构必须能往返序列化——它是前端与 API 的契约。
func TestFofaReconConfigJSONShape(t *testing.T) {
	cfg := fofaReconConfig{
		Enabled:     true,
		Keys:        []fofaKeyEntry{{Masked: "7bb4****c612"}},
		MaxAttempts: 3, AccountCool: 1800, NetworkCool: 30,
		Installed: true, MCPEnabled: true, ToolCached: 2,
	}
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, k := range []string{"enabled", "keys", "max_attempts", "account_cooldown", "network_cooldown", "tool_cached"} {
		if _, ok := back[k]; !ok {
			t.Fatalf("missing field %q in %s", k, raw)
		}
	}
	// value 必须省略（读取路径绝不回显明文）。
	keys := back["keys"].([]any)
	if _, has := keys[0].(map[string]any)["value"]; has {
		t.Fatal("read response must not include key value")
	}
}
