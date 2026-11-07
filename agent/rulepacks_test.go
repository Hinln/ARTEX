package agent

import (
	"strings"
	"testing"
)

// The two shipped packs must exist, be addressable by key, and stay disjoint in
// their grading vocabulary — a mismatch here would silently mis-grade findings.
func TestBuiltinRulePacksShape(t *testing.T) {
	packs := BuiltinRulePacks()
	if len(packs) != 2 {
		t.Fatalf("expected edusrc + enterprise, got %d", len(packs))
	}
	validLevels := map[string]bool{"critical": true, "high": true, "medium": true, "low": true}
	for _, p := range packs {
		if p.Key == "" || p.Name == "" || p.Scope == "" || p.Proof == "" {
			t.Fatalf("pack %q missing required text fields", p.Key)
		}
		if len(p.Accept) == 0 || len(p.Reject) == 0 || len(p.Grades) != 4 {
			t.Fatalf("pack %q: accept=%d reject=%d grades=%d", p.Key, len(p.Accept), len(p.Reject), len(p.Grades))
		}
		seen := map[string]bool{}
		for _, g := range p.Grades {
			if !validLevels[g.Level] {
				t.Fatalf("pack %q has unknown level %q", p.Key, g.Level)
			}
			if seen[g.Level] {
				t.Fatalf("pack %q repeats level %q", p.Key, g.Level)
			}
			seen[g.Level] = true
			if len(g.Criteria) == 0 {
				t.Fatalf("pack %q level %q has no criteria", p.Key, g.Level)
			}
		}
		if !validLevels[p.ReproduceMinSeverity] {
			t.Fatalf("pack %q ReproduceMinSeverity=%q", p.Key, p.ReproduceMinSeverity)
		}
	}
	if _, ok := RulePackByKey("EduSRC"); !ok {
		t.Fatal("lookup must be case-insensitive")
	}
	if _, ok := RulePackByKey("nope"); ok {
		t.Fatal("unknown key must not resolve")
	}
}

// Every reject entry becomes a HARD deny constraint, so it must read as a
// prohibition — an imperative sentence, not a description of what the class is.
func TestRejectEntriesReadAsProhibitions(t *testing.T) {
	for _, p := range BuiltinRulePacks() {
		for _, r := range p.Reject {
			if !strings.HasPrefix(r, "不") && !strings.HasPrefix(r, "禁止") && !strings.HasPrefix(r, "越权") {
				t.Fatalf("pack %q reject entry is not phrased as a rule: %q", p.Key, r)
			}
		}
	}
}

// The rendered block must carry grading criteria and the evidence contract, and
// must NOT duplicate the reject list (that lives in the deny constraints).
func TestRulePackPromptIsCompactAndComplete(t *testing.T) {
	for _, p := range BuiltinRulePacks() {
		out := rulePackPrompt(p)
		for _, g := range p.Grades {
			if !strings.Contains(out, g.Level) {
				t.Fatalf("pack %q: rendered block missing level %q", p.Key, g.Level)
			}
		}
		if !strings.Contains(out, p.Scope) {
			t.Fatalf("pack %q: rendered block missing scope", p.Key)
		}
		if !strings.Contains(out, "取证要求") {
			t.Fatalf("pack %q: rendered block missing proof contract", p.Key)
		}
		if !strings.Contains(out, "不得绕过") {
			t.Fatalf("pack %q: rendered block must point at the hard constraints", p.Key)
		}
		// Reject entries must not be copied into the prompt.
		for _, r := range p.Reject {
			if strings.Contains(out, r) {
				t.Fatalf("pack %q: reject entry duplicated into prompt (%q)", p.Key, r)
			}
		}
		// Injected every round: keep it bounded.
		if n := len([]rune(out)); n > 2600 {
			t.Fatalf("pack %q rendered block is %d runes, too long for per-round injection", p.Key, n)
		}
	}
}

// Severity must be expressed as exactly the four ARTEX levels, in descending
// order, so a caller can map a finding to a level without parsing text.
func TestGradesAreDescendingFourLevels(t *testing.T) {
	want := []string{"critical", "high", "medium", "low"}
	for _, p := range BuiltinRulePacks() {
		for i, g := range p.Grades {
			if g.Level != want[i] {
				t.Fatalf("pack %q grade[%d]=%q, want %q", p.Key, i, g.Level, want[i])
			}
		}
	}
}

func TestRulePacksCarryMustDoObligations(t *testing.T) {
	for _, pack := range BuiltinRulePacks() {
		if len(pack.Requires) == 0 {
			t.Fatalf("%s has no Requires", pack.Key)
		}
		block := rulePackPrompt(pack)
		if !strings.Contains(block, "必做") {
			t.Fatalf("%s prompt lacks the must-do section", pack.Key)
		}
		for _, r := range pack.Requires {
			if !strings.Contains(block, r) {
				t.Fatalf("%s prompt drops requirement %q", pack.Key, r)
			}
		}
	}
}
