package agent

import (
	"strings"
	"testing"
)

// §6.1 budget: a digest body is model-written prose with no length guarantee.
// Inlining it whole made the overview carry up to ~20K chars per planner round on
// a heavy task. The inline view keeps a head (marked as truncated) and the full
// body stays reachable through expand_digest.
func TestDigestHeadTruncatesAndMarks(t *testing.T) {
	short := "这是一段完整的短结论（#1,#2）。"
	if got, cut := digestHead(short); got != short || cut {
		t.Fatalf("short body must pass through unchanged: %q cut=%v", got, cut)
	}

	long := strings.Repeat("结论", coldDigestBodyMax) // 2 runes each → far over the cap
	got, cut := digestHead(long)
	if !cut {
		t.Fatal("an over-budget body must be marked truncated")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated head must end with an ellipsis: %q", got)
	}
	if n := len([]rune(got)); n > coldDigestBodyMax+1 {
		t.Fatalf("head is %d runes, cap is %d (+ellipsis)", n, coldDigestBodyMax)
	}
	if strings.ContainsRune(got, '�') {
		t.Fatal("truncation must not split a multi-byte rune")
	}
}

// The window default is the direction that decides whether compaction ever fires
// in time. Guessing high (the old 200K) meant a 128K model only compacted at
// ~167K, i.e. after the provider had already rejected the request.
func TestCompactionWindowDefaultsConservatively(t *testing.T) {
	if got, want := (Config{}).CompactionWindow(), defaultWindowK*1000; got != want {
		t.Fatalf("unset window = %d, want %d", got, want)
	}
	// A configured value is honoured, and clamping never raises it to the default.
	if got, want := (Config{ContextWindowK: 16}).CompactionWindow(), minWindowK*1000; got != want {
		t.Fatalf("tiny window = %d, want floor %d", got, want)
	}
	if got, want := (Config{ContextWindowK: 5000}).CompactionWindow(), maxWindowK*1000; got != want {
		t.Fatalf("huge window = %d, want cap %d", got, want)
	}
	if got, want := (Config{ContextWindowK: 128}).CompactionWindow(), 128_000; got != want {
		t.Fatalf("128K window = %d, want %d", got, want)
	}
}
