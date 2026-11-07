package server

import (
	"os"
	"testing"
)

func TestTokenBudgetKnob(t *testing.T) {
	cases := map[string]int64{"": 0, "5000": 5000, "abc": 0, "-5": 0, " 12 ": 12}
	for in, want := range cases {
		if in == "" {
			os.Unsetenv("ARTEX_TASK_TOKEN_BUDGET")
		} else {
			os.Setenv("ARTEX_TASK_TOKEN_BUDGET", in)
		}
		if got := tokenBudget(); got != want {
			t.Fatalf("tokenBudget(%q) = %d, want %d", in, got, want)
		}
	}
	os.Unsetenv("ARTEX_TASK_TOKEN_BUDGET")
}
