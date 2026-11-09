package guard

import (
	"net"
	"strings"
	"sync/atomic"
	"time"
)

// 私网目标不是禁区：真正的渗透里，从一台机器打进去之后看到的就是 10/172/192
// 的内网。禁区只有两类——
//
//  1. 我们自己脚下的基础设施（本机回环、云元数据、Docker 默认桥、自身容器所在网段）；
//  2. 没登记过的私网地址：先登记（add_task_scope），再由人批准，才允许动。
//
// 判 2 需要知道"这个地址是否在本任务的授权范围里"，那是 DB 的事，所以这里只留一个
// 由 server 注入的回调查询。

// PrivateScopeLookup 报告 session 所在任务的授权范围是否包含 host。
type PrivateScopeLookup func(session, host string) bool

var privateScopeLookup atomic.Pointer[PrivateScopeLookup]

// SetPrivateScopeLookup 由 server 在装配时注入；未注入时按"未授权"处理。
func SetPrivateScopeLookup(fn PrivateScopeLookup) {
	privateScopeLookup.Store(&fn)
}

func scopeAllowsPrivate(session, host string) bool {
	p := privateScopeLookup.Load()
	if p == nil || *p == nil {
		return false
	}
	return (*p)(session, host)
}

// selfInfra 是永远不能碰的那一小撮：碰了不是越权而是打自己。
var selfInfraNets = []string{
	"127.0.0.0/8",
	"::1/128",
	"169.254.169.254/32", // 云元数据
	"172.17.0.0/16",      // Docker 默认桥
	"fd00:ec2::254/128",  // AWS IPv6 元数据
}

var selfInfraHosts = map[string]bool{
	"localhost":                true,
	"host.docker.internal":     true,
	"gateway.docker.internal":  true,
	"metadata":                 true,
	"metadata.google.internal": true,
}

var selfInfraParsed []*net.IPNet

func init() {
	for _, c := range selfInfraNets {
		if _, n, err := net.ParseCIDR(c); err == nil {
			selfInfraParsed = append(selfInfraParsed, n)
		}
	}
}

// SelfInfraReason 说明这个地址为什么绝对不能碰；为空表示不是自家基础设施。
func SelfInfraReason(host string) string {
	host = strings.TrimSpace(strings.ToLower(stripPort(strings.TrimSpace(host))))
	if host == "" {
		return ""
	}
	if selfInfraHosts[host] {
		return "这是 ARTEX 自身/宿主机的别名，打它等于打自己"
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return ""
	}
	if ip.IsLoopback() {
		return "回环地址，指向 ARTEX 自己"
	}
	for _, n := range selfInfraParsed {
		if n.Contains(ip) {
			return "云元数据或本机 Docker 默认桥，属于 ARTEX 脚下的基础设施"
		}
	}
	return ""
}

func stripPort(v string) string {
	if h, _, err := net.SplitHostPort(v); err == nil {
		return h
	}
	return strings.Trim(v, "[]")
}

// privateTTL 只用于审核通过后的临时放行缓存，避免每一步都查库。
const privateTTL = 30 * time.Second
