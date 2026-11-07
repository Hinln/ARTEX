package agent

import (
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// TestFingerprintLive checks the signature table against a real Chinese site;
// run with ARTEX_LIVE_RECON=1.
func TestFingerprintLive(t *testing.T) {
	if os.Getenv("ARTEX_LIVE_RECON") != "1" {
		t.Skip("set ARTEX_LIVE_RECON=1 to fetch a live page")
	}
	target := "https://www.jcxjg.edu.cn/"
	client := &http.Client{Timeout: 20 * time.Second}
	req, _ := http.NewRequest("GET", target, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ARTEX)")
	resp, err := client.Do(req)
	if err != nil {
		t.Skipf("target unreachable: %v", err)
	}
	defer resp.Body.Close()
	buf := make([]byte, 512*1024)
	n, _ := resp.Body.Read(buf)
	body := string(buf[:n])
	headers := map[string]string{}
	for k, v := range resp.Header {
		headers[strings.ToLower(k)] = strings.Join(v, ", ")
	}
	hits := techHits(headers, body)
	if len(hits) == 0 {
		t.Fatalf("no signature fired on %s", target)
	}
	t.Logf("%s -> %v", target, hits)
}
