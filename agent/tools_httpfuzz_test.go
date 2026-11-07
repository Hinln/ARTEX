package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
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

// TestFuzzAgainstLocalServer drives the real request path: the handler reflects
// the value and blows up on a quote, which is exactly what the classifier must
// separate from noise.
func TestFuzzAgainstLocalServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v := r.URL.Query().Get("id")
		if strings.Contains(v, "'") {
			w.WriteHeader(500)
			_, _ = w.Write([]byte("You have an error in your SQL syntax; check the manual"))
			return
		}
		_, _ = w.Write([]byte("item id=" + v + " ok"))
	}))
	defer srv.Close()

	base, _ := url.Parse(srv.URL + "/item?id=1")
	client := &http.Client{}

	baseline, err := fuzzOnce(context.Background(), client, "GET", base, "query", "id", "", "1", "")
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if baseline.Status != 200 || baseline.SQLError != "" {
		t.Fatalf("baseline polluted: %+v", baseline)
	}

	quote, err := fuzzOnce(context.Background(), client, "GET", base, "query", "id", "", "' OR '1'='1", "")
	if err != nil {
		t.Fatalf("payload: %v", err)
	}
	quote.Payload = "' OR '1'='1"
	if quote.SQLError == "" {
		t.Fatalf("sql error not detected: %+v", quote)
	}
	if flags := fuzzFlags(baseline, quote); len(flags) < 2 {
		t.Fatalf("expected status + error flags, got %v", flags)
	}

	clean, _ := fuzzOnce(context.Background(), client, "GET", base, "query", "id", "", "2", "")
	clean.Payload = "2"
	if flags := fuzzFlags(baseline, clean); len(flags) != 0 {
		t.Fatalf("clean value flagged: %v", flags)
	}
}

func TestFuzzStampsCaseHeaderForLookup(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-Artex-Case")
		_, _ = w.Write([]byte("ok"))
	}))
	defer srv.Close()
	base, _ := url.Parse(srv.URL + "/x?p=1")
	if _, err := fuzzOnce(context.Background(), &http.Client{}, "GET", base, "query", "p", "", "v", "fz123-7"); err != nil {
		t.Fatalf("fuzzOnce: %v", err)
	}
	if got != "fz123-7" {
		t.Fatalf("case header not stamped, got %q", got)
	}
}
