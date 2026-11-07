package server

// Unit tests for the reproduction decision logic. These cover the parts that
// decide a verdict, which is where a wrong answer actually costs something: a
// false "passed" lets a hallucinated finding look verified, and a false "failed"
// silently downgrades a real one.

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// Only severities at or above the pack threshold require verification. Chinese
// labels count: an agent writing 严重 means the same thing as critical, and
// matching bare English literals let it skip the gate entirely. An unrecognised
// label fails closed — an extra replay costs a request, a skipped one costs the
// credibility of the whole finding list.
func TestReproducerNeeded(t *testing.T) {
	r := &Reproducer{minLevel: "high"}
	for _, sev := range []string{"critical", "high", "CRITICAL", " High ", "严重", "高危", "严重漏洞", "", "unrated"} {
		if !r.Needed(sev) {
			t.Fatalf("%q must require reproduction", sev)
		}
	}
	for _, sev := range []string{"medium", "low", "info", "中危", "低危", "信息"} {
		if r.Needed(sev) {
			t.Fatalf("%q must not require reproduction", sev)
		}
	}
	// The floor belongs to the pack, not to a constant: a pack asking for medium
	// verification gets it.
	med := &Reproducer{minLevel: "medium"}
	if !med.Needed("中危") {
		t.Fatal("medium floor must require medium findings")
	}
	med.SetMinLevel("")
	if med.minLevel != "medium" {
		t.Fatalf("empty level must keep the current floor, got %q", med.minLevel)
	}
}

// Replaying a destructive request is what turns a verification step into an
// incident; every write/delete shape must be refused.
func TestUnsafeToReplay(t *testing.T) {
	unsafe := []struct{ method, url string }{
		{"POST", "https://h/api/user/delete?id=1"},
		{"GET", "https://h/api/delete?id=2"},
		{"DELETE", "https://h/api/user/1"},
		{"PUT", "https://h/api/profile/update"},
		{"PATCH", "https://h/api/order/save"},
		{"POST", "https://h/api/admin/resetPassword"},
	}
	for _, c := range unsafe {
		if !unsafeToReplay(c.method, c.url) {
			t.Fatalf("%s %s must be treated as unsafe", c.method, c.url)
		}
	}
	safe := []struct{ method, url string }{
		{"GET", "https://h/api/users?id=1"},
		{"POST", "https://h/api/login"},              // auth probe, not a write to data
		{"POST", "https://h/api/search?q='or 1=1--"}, // injection probe
		{"HEAD", "https://h/assets/app.js"},
	}
	for _, c := range safe {
		if unsafeToReplay(c.method, c.url) {
			t.Fatalf("%s %s must be replayable", c.method, c.url)
		}
	}
}

// A WAF/challenge page must never be read as "the vulnerability is gone".
func TestChallengePagesAreNotFailures(t *testing.T) {
	pages := []string{
		"<html><title>Just a moment...</title></html>",
		"<html>请完成安全验证</html>",
		"Access denied: blocked by WAF",
		"<html><body>cf-challenge</body></html>",
	}
	for _, p := range pages {
		if !looksChallenge([]byte(p)) {
			t.Fatalf("challenge page not detected: %q", p)
		}
	}
	normal := []byte(`{"code":0,"data":[{"id":1,"idcard":"110101199001011234"}]}`)
	if looksChallenge(normal) {
		t.Fatal("a normal JSON body must not look like a challenge")
	}
	if looksChallenge(nil) {
		t.Fatal("an empty body must not look like a challenge")
	}
}

// Similarity must separate "same page" from "different page" cleanly, including
// the partial-echo case (same shell, different payload).
func TestSimilarityThresholds(t *testing.T) {
	// Build a body with HIGH ENTROPY so block-set matching cannot be fooled by
	// repeated content — a real page's rows differ from one another, and a test
	// built from a repeated literal would hide exactly the bug this guards.
	var sb strings.Builder
	for i := 0; i < 200; i++ {
		sb.WriteString(`{"id":`)
		sb.WriteString(strconv.Itoa(i))
		sb.WriteString(`,"phone":"1380000`)
		sb.WriteString(strconv.Itoa(1000 + i))
		sb.WriteString(`"},`)
	}
	base := []byte(sb.String())

	if got := similarity(base, base); got != 1 {
		t.Fatalf("identical bodies must score 1, got %f", got)
	}
	// One byte changed in the middle: still the same response.
	nudged := append([]byte{}, base...)
	nudged[len(nudged)/2] ^= 0x01
	if got := similarity(base, nudged); got < reproduceMatchRatio {
		t.Fatalf("one-byte change must still match, got %f", got)
	}
	// Same prefix, entirely different tail: must not match.
	tail := append(append([]byte{}, base[:len(base)/2]...), []byte(strings.Repeat("X", len(base)/2))...)
	if got := similarity(base, tail); got >= reproduceMatchRatio {
		t.Fatalf("different tail must not match, got %f", got)
	}
	if got := similarity(base, []byte("completely unrelated body")); got >= reproduceMatchRatio {
		t.Fatalf("unrelated body must not match, got %f", got)
	}
	if got := similarity(base, nil); got != 0 {
		t.Fatalf("empty vs non-empty must score 0, got %f", got)
	}
}

// Raw stored requests are `request-line, headers, blank line, body`; the parser
// must recover headers and body without the framing lines leaking into either.
func TestReadHTTPRequest(t *testing.T) {
	raw := "POST /api/users?id=1 HTTP/1.1\r\nHost: example.com\r\nCookie: a=b\r\n" +
		"Content-Type: application/json\r\nContent-Length: 13\r\n\r\n{\"id\":\"1\"}"
	head, body, err := readHTTPRequest(strings.NewReader(raw), reproduceMaxSource)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if string(body) != `{"id":"1"}` {
		t.Fatalf("body = %q", body)
	}
	if head.Get("Cookie") != "a=b" {
		t.Fatalf("header lost: %v", head)
	}
	if head.Get("Host") != "example.com" {
		t.Fatal("Host header must be parsed (it is dropped only at replay time)")
	}
	if strings.Contains(string(body), "Content-Length") {
		t.Fatal("framing header leaked into the body")
	}
}

// Replay headers must not carry framing/hop-by-hop values from the recording.
func TestCopyReplayHeadersDropsFraming(t *testing.T) {
	src := http.Header{}
	src.Set("Cookie", "session=abc")
	src.Set("Content-Length", "42")
	src.Set("Host", "recorded.example")
	src.Set("Connection", "keep-alive")
	src.Set("Accept-Encoding", "gzip")
	src.Set("X-Custom", "keep")

	dst := http.Header{}
	copyReplayHeaders(dst, src)

	if dst.Get("Cookie") != "session=abc" || dst.Get("X-Custom") != "keep" {
		t.Fatalf("substantive headers must survive: %v", dst)
	}
	for _, drop := range []string{"Content-Length", "Host", "Connection", "Accept-Encoding"} {
		if dst.Get(drop) != "" {
			t.Fatalf("%s must be dropped from a replay", drop)
		}
	}
}

// With no replayable binding the outcome must be "skipped", never "passed":
// silence is not evidence.
func TestNoBindingIsSkippedNotPassed(t *testing.T) {
	r := &Reproducer{}
	res, err := r.Reproduce(t.Context(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.State != "blocked" && res.State != "skipped" {
		t.Fatalf("no store must yield skipped/blocked, got %q", res.State)
	}
	if res.State == "passed" {
		t.Fatal("an unattempted reproduction must never read as passed")
	}
}
