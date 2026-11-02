package llmhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewClientUsesNativeSOCKS5Proxy(t *testing.T) {
	client, err := NewClient("socks5://user:pass@127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type=%T", client.Transport)
	}
	proxyURL, err := transport.Proxy(httptest.NewRequest(http.MethodGet, "https://target.invalid", nil))
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL == nil || proxyURL.Scheme != "socks5" || proxyURL.Host != "127.0.0.1" || proxyURL.User.String() != "user:pass" {
		t.Fatalf("proxy URL=%v", proxyURL)
	}
}
