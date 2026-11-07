package agent

import (
	"os"
	"testing"
)

func TestMurmur3KnownVectors(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
	}{
		{"", 0},
		{"hello", 0x248bfa47},
		{"The quick brown fox jumps over the lazy dog", 0x2e4ff723},
	}
	for _, c := range cases {
		if got := murmur3Sum32([]byte(c.in)); got != c.want {
			t.Fatalf("murmur3(%q)=%#x want %#x", c.in, got, c.want)
		}
	}
}

func TestParseRobotsKeepsPathsAndSitemaps(t *testing.T) {
	body := "User-agent: *\nDisallow: /admin/\nDisallow: /internal/api\nAllow: /public\nSitemap: https://x.com/sitemap.xml\n"
	disallow, allow, sitemaps := parseRobots(body)
	if len(disallow) != 2 || disallow[0] != "/admin/" {
		t.Fatalf("disallow=%v", disallow)
	}
	if len(allow) != 1 || allow[0] != "/public" {
		t.Fatalf("allow=%v", allow)
	}
	if len(sitemaps) != 1 || sitemaps[0] != "https://x.com/sitemap.xml" {
		t.Fatalf("sitemaps=%v", sitemaps)
	}
}

func TestParseRobotsDedupesAndSkipsRoot(t *testing.T) {
	body := "Disallow: /\nDisallow: /a\nDisallow: /a\n"
	disallow, _, _ := parseRobots(body)
	if len(disallow) != 1 || disallow[0] != "/a" {
		t.Fatalf("disallow=%v want [/a]", disallow)
	}
}

func TestParseSitemapLocsCapsAndDedupes(t *testing.T) {
	body := "<loc>https://x.com/a</loc><loc>https://x.com/a</loc><loc>https://x.com/b</loc>"
	locs := parseSitemapLocs(body, 10)
	if len(locs) != 2 || locs[0] != "https://x.com/a" || locs[1] != "https://x.com/b" {
		t.Fatalf("locs=%v", locs)
	}
}

func TestNormalizeDomain(t *testing.T) {
	ok := map[string]string{
		"*.Example.COM": "example.com",
		"a.example.com.": "a.example.com",
		" example.com ":  "example.com",
	}
	for in, want := range ok {
		if got := normalizeDomain(in); got != want {
			t.Fatalf("normalizeDomain(%q)=%q want %q", in, got, want)
		}
	}
	for _, bad := range []string{"", "localhost", "user@example.com", "http://x.com/a"} {
		if got := normalizeDomain(bad); got != "" {
			t.Fatalf("normalizeDomain(%q)=%q want empty", bad, got)
		}
	}
}

func TestFofaKeyPoolParsesPool(t *testing.T) {
	t.Setenv("FOFA_KEYS", "")
	os.Setenv("ARTEX_FOFA_KEYS", "a@x.com:key1, b@x.com:key2;c@x.com:key3")
	defer os.Unsetenv("ARTEX_FOFA_KEYS")
	got := fofaKeyPool()
	if len(got) != 3 {
		t.Fatalf("pool=%v", got)
	}
	if got[1].Email != "b@x.com" || got[1].Key != "key2" {
		t.Fatalf("entry=%+v", got[1])
	}
}

func TestFofaKeyPoolEmptyWhenUnset(t *testing.T) {
	t.Setenv("ARTEX_FOFA_KEYS", "")
	t.Setenv("FOFA_KEYS", "")
	if got := fofaKeyPool(); len(got) != 0 {
		t.Fatalf("pool=%v want empty", got)
	}
}

func TestReconToolSchemasAreRegistered(t *testing.T) {
	ts := &ToolSet{}
	tools := []struct {
		name string
		tool interface{ Name() string }
	}{
		{"cert_subdomains", ts.certSubdomains()},
		{"wayback_urls", ts.waybackURLs()},
		{"rdap_lookup", ts.rdapLookup()},
		{"reverse_ip", ts.reverseIP()},
		{"cdn_detect", ts.cdnDetect()},
		{"favicon_hash", ts.faviconHash()},
		{"fofa_search", ts.fofaSearch()},
		{"robots_harvest", ts.robotsHarvest()},
	}
	for _, c := range tools {
		if c.tool == nil {
			t.Fatalf("%s 未注册", c.name)
		}
	}
}
