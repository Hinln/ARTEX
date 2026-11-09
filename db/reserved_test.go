package db

import "testing"

func TestReservedReasonCoversTheIncidentRanges(t *testing.T) {
	deny := map[string]string{
		"172.26.0.8":   "RFC1918 私网（含 Docker bridge）",
		"172.21.0.1":   "RFC1918 私网（含 Docker bridge）",
		"10.1.2.3":     "RFC1918 私网",
		"192.168.1.1":  "RFC1918 私网",
		"127.0.0.1":    "回环地址",
		"169.254.169.254": "链路本地 / 云元数据",
		"100.64.0.9":   "CGNAT 共享地址",
		"198.18.0.5":   "Clash/CFW fake-ip 段",
		"::1":          "IPv6 回环",
	}
	for ip, want := range deny {
		if got := ReservedReason(ip); got != want {
			t.Fatalf("%s: want %q, got %q", ip, want, got)
		}
	}
}

func TestReservedReasonAllowsRealTargets(t *testing.T) {
	for _, ip := range []string{"162.105.205.252", "202.96.170.165", "8.8.8.8", "", "not-an-ip"} {
		if got := ReservedReason(ip); got != "" {
			t.Fatalf("%q must not be reserved, got %q", ip, got)
		}
	}
}

func TestReservedReasonAcceptsHostPort(t *testing.T) {
	if ReservedReason("172.26.0.8:8089") == "" {
		t.Fatal("host:port form must still be judged")
	}
	if ReservedReason("sign.phbs.pku.edu.cn:443") != "" {
		t.Fatal("a hostname is not an address; leave it to DNS checks")
	}
}

func TestReservedScopeReasonOnCIDR(t *testing.T) {
	if ReservedScopeReason("172.26.0.8/32") == "" {
		t.Fatal("single-host CIDR in private space must be reserved")
	}
	if ReservedScopeReason("162.105.0.0/16") != "" {
		t.Fatal("public /16 must pass")
	}
}
