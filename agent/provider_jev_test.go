package agent

import "testing"

func TestJevConfigCannotBuildChatProvider(t *testing.T) {
	c := ConfigFrom("jev", "", "", "key", "")
	if !c.Jev || c.Provider() != "jev" {
		t.Fatalf("unexpected Jev config: %+v", c)
	}
	if _, err := c.NewProvider(); err == nil {
		t.Fatal("Jev config must be rejected by chat provider construction")
	}
}
