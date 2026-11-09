package agent

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/intercept"
)

// allowPrivateTargets is the single escape hatch. Default off: a finding whose
// assets live in RFC1918 / reserved space is refused outright, because that is
// how a lab container on the same Docker host got reported as a customer's
// internal server.
func allowPrivateTargets() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("ARTEX_ALLOW_PRIVATE_TARGETS"))) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// reservedAssetsReason reports the first reserved address among the finding's
// assets, with a message that tells the worker what to do about it.
func (t *ToolSet) reservedAssetsReason(ctx context.Context, ids []int64) (string, bool) {
	if allowPrivateTargets() || t.as == nil || len(ids) == 0 {
		return "", false
	}
	assets, err := t.as.GetByIDs(ids)
	if err != nil {
		return "", false // 查不到资产不是拦结论的理由
	}
	for _, a := range assets {
		if a == nil {
			continue
		}
		for _, ip := range []string{a.IP, hostOfURL(a.URL)} {
			if db.ReservedReason(ip) == "" {
				continue
			}
			// 战果内网：范围表里已批准（source=agent/manual）就认。
			if t.ts != nil {
				if ok, err := t.ts.DB().ScopeAllowsHost(context.Background(), t.ts.ID(), ip); err == nil && ok {
					continue
				}
				// 全自动路径：已经在公网落地，且这个地址是从执行输出里看来的，就算战果。
				if foothold, err := t.ts.DB().HasPublicFoothold(context.Background(), t.ts.ID()); err == nil && foothold &&
					intercept.PrivateHostSeenInLedger(strconv.FormatInt(intercept.ConvIDFromContext(ctx), 10), ip) {
					continue
				}
			}
			if why := db.ReservedReason(ip); why != "" {
				return fmt.Sprintf(
					"这条结论落在 %s（%s），属于私网/保留地址，不是客户的外部资产：本机 Docker 靶场、宿主网关、云元数据都长这样。\n"+
						"堵住这条路的办法：① 在真正的目标域名/公网 IP 上重新取证；② 如果这确实是授权范围内的内网，先在任务范围里显式登记它（add_task_scope kind=ip），再由人工确认，别把靶场当客户交。",
					ip, why), true
			}
		}
	}
	return "", false
}

// hostOfURL returns the host of a URL (dropping the port), or "" when the value
// is not a URL. Service assets carry their address in the URL, not in IP.
func hostOfURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" || !strings.Contains(raw, "://") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
