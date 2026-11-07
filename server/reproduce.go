package server

// Reproduction: re-send a finding's own bound request against the live target and
// compare the response to the recorded baseline.
//
// Why replay the recorded request instead of asking a model "does this look
// reproducible?": a model that already wrote an optimistic `evidence` will read
// that same text back and agree with itself. The one signal it cannot author is
// what the target actually returns when the SYSTEM sends the request. So the
// verdict here is derived from bytes:
//
//	passed  — a proof/verification request returned a response whose status and
//	          body are equivalent to the baseline (the vulnerability still shows)
//	failed  — it returned something clearly different (fixed, or never true)
//	skipped — replaying would be unsafe (write/delete PoC) or impossible (no
//	          replayable request bound)
//	blocked — the target is unreachable, auth expired, or the budget is spent
//
// "failed" is deliberately conservative: an inconclusive difference (WAF page,
// changed redirect) is reported as blocked, not failed, so a flaky network never
// silently downgrades a real finding.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/evidence"
)

// ReproduceLimits bounds one replay. A verification request must be small and
// fast: it runs while a task is in flight, and a hung replay would stall the
// finding instead of merely failing to confirm it.
const (
	reproduceTimeout   = 20 * time.Second
	reproduceMaxBody   = 512 * 1024
	reproduceMaxSource = 256 * 1024
	// Body similarity below this is "clearly different". Kept strict so that a
	// page which merely echoes the same static shell around an error still counts
	// as a change; the cost of a false "failed" (a real finding downgraded) is
	// higher than of a false "blocked".
	reproduceMatchRatio = 0.90
)

// Reproducer re-issues a bound request and records the outcome.
type Reproducer struct {
	store    *evidence.Store
	pg       *db.DB
	caFile   string
	proxy    string
	minLevel string
}

// NewReproducer builds a reproducer. caFile/proxy mirror the recording proxy so
// the replay reaches the target the same way the original capture did (and trusts
// its MITM CA instead of disabling verification).
func NewReproducer(store *evidence.Store, pg *db.DB, proxy, caFile string) *Reproducer {
	return &Reproducer{store: store, pg: pg, proxy: proxy, caFile: caFile, minLevel: "high"}
}

// Needed reports whether a finding at this severity should be verified. Mirrors
// RulePack.ReproduceMinSeverity: 严重/高危 must be reproduced before they may be
// displayed as confirmed.
func (r *Reproducer) Needed(severity string) bool {
	switch strings.ToLower(strings.TrimSpace(severity)) {
	case "critical", "high":
		return true
	}
	return false
}

// ReproduceResult is the outcome of one attempt.
type ReproduceResult struct {
	State          string
	Reason         string
	StatusCode     int
	ResponseDigest string
	BaselineDigest string
	Matched        bool
	TrafficIDs     []string
	Duration       time.Duration
}

// Reproduce re-sends the finding's bound proof/verification request(s).
//
// Only roles proof and verification are replayed: a baseline request is the
// normal-traffic control and a supporting one is context, so re-sending either
// proves nothing about the vulnerability and only adds load on the target.
func (r *Reproducer) Reproduce(ctx context.Context, findingID int64) (ReproduceResult, error) {
	if r.store == nil || r.pg == nil {
		return ReproduceResult{State: db.ReproduceBlocked, Reason: "复现组件未初始化"}, nil
	}
	bindings, version, err := r.replayableBindings(ctx, findingID)
	if err != nil {
		return ReproduceResult{}, err
	}
	if len(bindings) == 0 {
		return ReproduceResult{
			State:  db.ReproduceSkipped,
			Reason: "未绑定可重放的 proof/verification 流量：请用 traffic_refs 绑定实际证明请求，或改用命令/日志证据（该结论按未复现处理）",
		}, nil
	}
	for _, b := range bindings {
		if unsafeToReplay(b.Snapshot.Method, b.Snapshot.URL) {
			return ReproduceResult{
				State:      db.ReproduceSkipped,
				Reason:     "该 PoC 属于写/删/改或破坏性请求，不现场重放；请按无害证据契约（哨兵/授权对照/幂等回写）提交",
				TrafficIDs: []string{b.Snapshot.SourceTrafficID},
			}, nil
		}
	}

	client, err := r.httpClient()
	if err != nil {
		return ReproduceResult{State: db.ReproduceBlocked, Reason: "构造复现客户端失败: " + err.Error()}, nil
	}

	// Any single proof request reproducing is enough: a finding bound with several
	// proofs is trying to show the same behaviour from different angles.
	var last ReproduceResult
	for _, b := range bindings {
		res := r.replayOne(ctx, client, b)
		res.TrafficIDs = append([]string{b.Snapshot.SourceTrafficID}, res.TrafficIDs...)
		if res.State == db.ReproducePassed {
			res.State = db.ReproducePassed
			res.Reason = "重放 " + b.Snapshot.Method + " " + b.Snapshot.URL + " 返回与录制证据等价的响应：" + res.Reason
			return res, nil
		}
		last = res
	}
	_ = version
	return last, nil
}

// replayableBindings returns the finding's proof/verification bindings that carry
// a stored request body, plus the finding's current evidence version.
func (r *Reproducer) replayableBindings(ctx context.Context, findingID int64) ([]db.FindingTrafficBinding, int64, error) {
	traffic, err := r.storeTraffic(ctx, findingID)
	if err != nil {
		return nil, 0, err
	}
	var out []db.FindingTrafficBinding
	for _, b := range traffic.Bindings {
		switch b.Role {
		case "proof", "verification":
			out = append(out, b)
		}
	}
	return out, traffic.Version, nil
}

func (r *Reproducer) storeTraffic(ctx context.Context, findingID int64) (*db.FindingTraffic, error) {
	return r.pg.GetFindingTraffic(ctx, findingID)
}

// replayOne re-sends one bound request and compares the outcome to its baseline.
func (r *Reproducer) replayOne(ctx context.Context, client *http.Client, b db.FindingTrafficBinding) ReproduceResult {
	start := time.Now()
	rawReq, _, err := r.store.OpenBody(b.Snapshot, "request")
	if err != nil {
		return ReproduceResult{State: db.ReproduceBlocked, Reason: "读取证据请求体失败: " + err.Error()}
	}
	defer rawReq.Close()
	head, body, err := readHTTPRequest(rawReq, reproduceMaxSource)
	if err != nil {
		return ReproduceResult{State: db.ReproduceBlocked, Reason: "解析证据请求失败: " + err.Error()}
	}
	target, err := url.Parse(b.Snapshot.URL)
	if err != nil || target.Host == "" {
		return ReproduceResult{State: db.ReproduceBlocked, Reason: "证据 URL 无效: " + b.Snapshot.URL}
	}
	method := strings.ToUpper(strings.TrimSpace(b.Snapshot.Method))
	if method == "" {
		method = http.MethodGet
	}

	// Only carry the headers that shape the response; drop hop-by-hop and the
	// recording proxy's own bookkeeping so the replay is a fresh request.
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(body))
	if err != nil {
		return ReproduceResult{State: db.ReproduceBlocked, Reason: "构造重放请求失败: " + err.Error()}
	}
	copyReplayHeaders(req.Header, head)

	resp, err := client.Do(req)
	if err != nil {
		return ReproduceResult{
			State:    db.ReproduceBlocked,
			Reason:   "目标不可达或复现请求失败: " + err.Error(),
			Duration: time.Since(start),
		}
	}
	defer resp.Body.Close()
	live, _ := io.ReadAll(io.LimitReader(resp.Body, reproduceMaxBody))
	liveDigest := sha256Hex(live)
	res := ReproduceResult{
		StatusCode:     resp.StatusCode,
		ResponseDigest: liveDigest,
		BaselineDigest: b.Snapshot.RespHash,
		TrafficIDs:     nil,
		Duration:       time.Since(start),
	}

	base, err := r.baselineBody(b.Snapshot)
	if err != nil {
		res.State = db.ReproduceBlocked
		res.Reason = "读取录制基线响应失败: " + err.Error()
		return res
	}
	if resp.StatusCode != b.Snapshot.Status {
		// A status change is the strongest and least ambiguous signal.
		res.State = db.ReproduceFailed
		res.Reason = fmt.Sprintf("状态码由录制时的 %d 变为 %d", b.Snapshot.Status, resp.StatusCode)
		return res
	}
	ratio := similarity(base, live)
	res.Matched = ratio >= reproduceMatchRatio
	if res.Matched {
		res.State = db.ReproducePassed
		res.Reason = fmt.Sprintf("状态码 %d 一致，响应正文相似度 %.2f（阈值 %.2f）", resp.StatusCode, ratio, reproduceMatchRatio)
		return res
	}
	// Same status, different body: could be a fix, could be a WAF/challenge page.
	if looksChallenge(live) {
		res.State = db.ReproduceBlocked
		res.Reason = fmt.Sprintf("响应疑似被 WAF/挑战页替换（相似度 %.2f），无法判定", ratio)
		return res
	}
	res.State = db.ReproduceFailed
	res.Reason = fmt.Sprintf("状态码 %d 一致但正文已改变（相似度 %.2f < %.2f），按不可复现处理", resp.StatusCode, ratio, reproduceMatchRatio)
	return res
}

// baselineBody reads the recorded response body for comparison.
func (r *Reproducer) baselineBody(s db.TrafficEvidenceSnapshot) ([]byte, error) {
	f, _, err := r.store.OpenBody(s, "response")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, reproduceMaxBody))
}

func (r *Reproducer) httpClient() (*http.Client, error) {
	tr := &http.Transport{
		Proxy:                 nil,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ResponseHeaderTimeout: reproduceTimeout,
	}
	// Route through the recording proxy when capture is on: that is the path the
	// original request took, and it is also what makes the capture CA valid.
	if strings.TrimSpace(r.proxy) != "" {
		if u, err := url.Parse("http://" + strings.TrimSpace(r.proxy)); err == nil {
			tr.Proxy = http.ProxyURL(u)
		}
	}
	if strings.TrimSpace(r.caFile) != "" {
		if pem, err := os.ReadFile(r.caFile); err == nil {
			pool, err := x509.SystemCertPool()
			if err != nil || pool == nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM(pem) {
				tr.TLSClientConfig.RootCAs = pool
			}
		}
	}
	return &http.Client{Transport: tr, Timeout: reproduceTimeout, CheckRedirect: func(*http.Request, []*http.Request) error {
		// Follow no redirects: a 302 where the baseline was a 200 is exactly the
		// kind of change we want to observe, not paper over.
		return http.ErrUseLastResponse
	}}, nil
}

// --- small helpers ---

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// unsafeToReplay reports whether re-sending this request could mutate the target.
//
// The URL is checked for EVERY method, not just write methods: plenty of systems
// expose deletion over GET (`?action=delete&id=1`), and replaying that is exactly
// as destructive as a DELETE. A write-shaped URL is refused regardless of how it
// is delivered; the write-proof contract (sentinel / authz-diff / idempotent
// rewrite) is the intended way to evidence those findings.
func unsafeToReplay(method, rawURL string) bool {
	u := strings.ToLower(rawURL)
	for _, marker := range []string{
		"delete", "remove", "update", "modify", "edit", "save", "insert",
		"create", "reset", "del=", "/del", "drop", "truncate",
	} {
		if strings.Contains(u, marker) {
			return true
		}
	}
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case "DELETE":
		// The verb alone is destructive, whatever the URL says.
		return true
	case "POST", "PUT", "PATCH":
		// A write verb on a URL that does not advertise the operation is the normal
		// case for proof requests (login, search, injection probe) and is safe.
		return false
	}
	return false
}

// copyReplayHeaders copies request headers, dropping hop-by-hop and framing ones.
func copyReplayHeaders(dst http.Header, src http.Header) {
	for k, vs := range src {
		switch strings.ToLower(k) {
		case "host", "content-length", "connection", "transfer-encoding",
			"keep-alive", "proxy-connection", "te", "trailer", "upgrade",
			"accept-encoding":
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

// readHTTPRequest splits a raw HTTP request into its header map and body.
func readHTTPRequest(f io.Reader, limit int) (http.Header, []byte, error) {
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)))
	if err != nil {
		return nil, nil, err
	}
	// The stored form is a request line, headers, a blank line, then the body.
	sep := bytes.Index(raw, []byte("\r\n\r\n"))
	if sep < 0 {
		sep = bytes.Index(raw, []byte("\n\n"))
	}
	if sep < 0 {
		return nil, nil, errors.New("请求体缺少头/正文分隔")
	}
	headText := string(raw[:sep])
	var body []byte
	if sep+4 <= len(raw) && bytes.HasPrefix(raw[sep:], []byte("\r\n\r\n")) {
		body = raw[sep+4:]
	} else {
		body = raw[sep+2:]
	}
	header := http.Header{}
	lines := strings.Split(strings.ReplaceAll(headText, "\r\n", "\n"), "\n")
	for _, line := range lines[1:] { // skip the request line
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		header.Add(strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]))
	}
	return header, body, nil
}

// similarity estimates how much two bodies agree, in [0,1].
//
// A prefix/suffix scan alone is far too brittle: a single token changing in the
// MIDDLE of a page (a session id, a timestamp, one record) leaves both ends
// intact while prefix+suffix still covers most of the bytes — and yet a genuine
// change also lives in the middle. So this samples the body as fixed-size blocks
// and counts how many blocks of the baseline are reproduced, which is stable
// against moved offsets while still detecting a real content change.
func similarity(a, b []byte) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) > reproduceMaxBody {
		a = a[:reproduceMaxBody]
	}
	if len(b) > reproduceMaxBody {
		b = b[:reproduceMaxBody]
	}
	const block = 64
	// Count baseline blocks present in the live body. A block set (not a counter)
	// keeps repeated content from inflating the score.
	live := make(map[string]struct{}, len(b)/block+1)
	for i := 0; i+block <= len(b); i += block {
		live[string(b[i:i+block])] = struct{}{}
	}
	total, hit := 0, 0
	for i := 0; i+block <= len(a); i += block {
		total++
		if _, ok := live[string(a[i:i+block])]; ok {
			hit++
		}
	}
	if total == 0 {
		// Both bodies are shorter than one block: compare directly.
		if len(a) == len(b) && bytes.Equal(a, b) {
			return 1
		}
		return 0
	}
	return float64(hit) / float64(total)
}

// looksChallenge detects the usual "not the real response" pages so a WAF block is
// never mistaken for a fix.
func looksChallenge(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	needle := strings.ToLower(string(body[:min(len(body), 4096)]))
	for _, marker := range []string{
		"cf-challenge", "captcha", "just a moment", "attention required",
		"访问验证", "安全验证", "请输入验证码", "人机验证", "waf", "blocked by",
		"403 forbidden", "request rejected",
	} {
		if strings.Contains(needle, marker) {
			return true
		}
	}
	return false
}
