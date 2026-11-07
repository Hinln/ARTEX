package agent

import (
	"net/url"
	"strings"
	"testing"
)

func TestFuzzDefaultPayloadsCoverTheClasses(t *testing.T) {
	joined := strings.Join(defaultFuzzPayloads, "\n")
	for _, want := range []string{"' OR '1'='1", "SLEEP(3)", "<script>", "${7*7}", "etc/passwd", "|id"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("payload set missing %q", want)
		}
	}
	if len(defaultFuzzPayloads) > 40 {
		t.Fatalf("payload set grew into a dictionary: %d", len(defaultFuzzPayloads))
	}
}

func TestFuzzFlagsOnlyReportsRealDiffs(t *testing.T) {
	base := fuzzCase{Status: 200, Length: 1000, Millis: 120}

	same := fuzzCase{Status: 200, Length: 1010, Millis: 130}
	if flags := fuzzFlags(base, same); len(flags) != 0 {
		t.Fatalf("noise flagged: %v", flags)
	}

	sql := fuzzCase{Status: 500, Length: 1400, Millis: 150, SQLError: "You have an error in your SQL syntax"}
	if flags := fuzzFlags(base, sql); len(flags) == 0 {
		t.Fatalf("sql error not flagged")
	}

	blind := fuzzCase{Status: 200, Length: 1000, Millis: 3500, Payload: "1 AND SLEEP(3)"}
	if flags := fuzzFlags(base, blind); len(flags) == 0 || !strings.Contains(strings.Join(flags, " "), "时差") {
		t.Fatalf("time-based blind not flagged: %v", flags)
	}

	slow := fuzzCase{Status: 200, Length: 1000, Millis: 3500, Payload: "harmless"}
	if flags := fuzzFlags(base, slow); len(flags) != 0 {
		t.Fatalf("slow-but-harmless payload flagged: %v", flags)
	}

	reflect := fuzzCase{Status: 200, Length: 1000, Millis: 120, Reflect: true, Payload: "${7*7}"}
	if flags := fuzzFlags(base, reflect); len(flags) == 0 {
		t.Fatalf("reflection not flagged")
	}
}

func TestFuzzRequestBuilders(t *testing.T) {
	base, _ := url.Parse("https://x.test/item?id=1&cat=2")

	req, err := newFuzzRequest("GET", base, "query", "id", "", "'")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(req.URL.RawQuery, "id=%27") {
		t.Fatalf("query not replaced: %s", req.URL.RawQuery)
	}
	if !strings.Contains(req.URL.RawQuery, "cat=2") {
		t.Fatalf("sibling param dropped: %s", req.URL.RawQuery)
	}

	bodyReq, err := newFuzzRequest("POST", base, "body", "id", "", "1'")
	if err != nil {
		t.Fatal(err)
	}
	if bodyReq.Method != "POST" || bodyReq.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
		t.Fatalf("body request shape wrong: %s %s", bodyReq.Method, bodyReq.Header.Get("Content-Type"))
	}

	headReq, err := newFuzzRequest("GET", base, "header", "X-Forwarded-For", "X-Forwarded-For", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if headReq.Header.Get("X-Forwarded-For") != "127.0.0.1" {
		t.Fatalf("header not set")
	}

	if _, err := newFuzzRequest("GET", base, "cookie", "id", "", "x"); err == nil {
		t.Fatalf("unknown location accepted")
	}
}

func TestSanitizeURLMasksSecrets(t *testing.T) {
	got := sanitizeURL("https://x.test/a?token=abc&key=def&id=1")
	if strings.Contains(got, "abc") || strings.Contains(got, "def") {
		t.Fatalf("secret leaked: %s", got)
	}
	if !strings.Contains(got, "id=1") {
		t.Fatalf("useful param dropped: %s", got)
	}
}
