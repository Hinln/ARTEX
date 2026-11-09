package db

import (
	"net"
	"strings"
)

// reservedNets are address blocks that are never a customer's external asset:
// RFC1918 private space, loopback, link-local (cloud metadata), CGNAT, the
// multicast/broadcast tail, and the fake-IP range local proxies hand out.
//
// The incident this guards against: a teaching lab published on the same Docker
// host sat in 172.26.0.0/16. It was reachable, so the agent registered it as an
// authorized asset, then reported 17 findings against its own host's containers.
// Reachability is not authorization.
var reservedNets = func() []struct {
	net  *net.IPNet
	note string
} {
	specs := []struct{ cidr, note string }{
		{"10.0.0.0/8", "RFC1918 私网"},
		{"172.16.0.0/12", "RFC1918 私网（含 Docker bridge）"},
		{"192.168.0.0/16", "RFC1918 私网"},
		{"127.0.0.0/8", "回环地址"},
		{"169.254.0.0/16", "链路本地 / 云元数据"},
		{"100.64.0.0/10", "CGNAT 共享地址"},
		{"0.0.0.0/8", "本网络"},
		{"224.0.0.0/3", "组播与保留段"},
		{"198.18.0.0/15", "Clash/CFW fake-ip 段"},
		{"fc00::/7", "IPv6 ULA"},
		{"fe80::/10", "IPv6 链路本地"},
		{"::1/128", "IPv6 回环"},
	}
	out := make([]struct {
		net  *net.IPNet
		note string
	}, 0, len(specs))
	for _, s := range specs {
		_, n, err := net.ParseCIDR(s.cidr)
		if err != nil {
			continue
		}
		out = append(out, struct {
			net  *net.IPNet
			note string
		}{n, s.note})
	}
	return out
}()

// ReservedReason returns a human reason when ip falls in a block that must never
// be treated as an external asset, or "" when it is a normal public address.
// Host:port and [v6]:port forms are accepted; empty input is not reserved.
func ReservedReason(ip string) string {
	ip = stripHostPort(strings.TrimSpace(ip))
	if ip == "" {
		return ""
	}
	p := net.ParseIP(ip)
	if p == nil {
		return ""
	}
	for _, r := range reservedNets {
		if r.net.Contains(p) {
			return r.note
		}
	}
	return ""
}

// ReservedScopeReason is ReservedReason for a scope/net value that may already be
// in CIDR form ("172.26.0.8/32"). Empty when not reserved or unparseable.
func ReservedScopeReason(network string) string {
	network = strings.TrimSpace(network)
	if network == "" {
		return ""
	}
	if _, n, err := net.ParseCIDR(network); err == nil {
		// Judge by the network's first address; a /8 that merely *contains*
		// reserved space is still a real external target.
		return ReservedReason(n.IP.String())
	}
	return ReservedReason(network)
}
