package jev

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Autumn-27/artex/intercept"
)

func TestBuildRequestMapsReviewInputAndChoiceCriteria(t *testing.T) {
	req, err := BuildRequest(intercept.ReviewInput{Version: 5, Tool: "Bash", Arguments: json.RawMessage(`{"cmd":"ls"}`)}, "policy text", "jev-test")
	if err != nil {
		t.Fatal(err)
	}
	if req.Model != "jev-test" || req.Questions["decision"].Type != "choice" {
		t.Fatalf("unexpected request: %#v", req)
	}
	b, _ := json.Marshal(req.State)
	if !strings.Contains(string(b), "Bash") || strings.Contains(string(b), "policy text") {
		t.Fatalf("state should contain evidence only: %s", b)
	}
	if !strings.Contains(req.Questions["decision"].Instructions, "policy text") {
		t.Fatalf("custom policy missing from instructions: %s", req.Questions["decision"].Instructions)
	}
	if len(b) > maxStateBytes+2000 {
		t.Fatalf("state exceeded bound: %d", len(b))
	}
}

func TestBuildRequestUsesCompactPolicyForDefaultPrompts(t *testing.T) {
	for _, prompt := range []string{"", intercept.DefaultJudgePrompt, intercept.EffectiveJudgePrompt(intercept.DefaultJudgePrompt)} {
		req, err := BuildRequest(intercept.ReviewInput{Version: 5, Tool: "Bash", Arguments: json.RawMessage(`{"cmd":"cat /etc/shadow"}`)}, prompt, "")
		if err != nil {
			t.Fatal(err)
		}
		instructions := req.Questions["decision"].Instructions
		if !strings.Contains(instructions, "漏洞利用 payload") || !strings.Contains(instructions, "其余默认 allow") {
			t.Fatalf("default policy lost authorized-pentest semantics: %s", instructions)
		}
		if strings.Contains(instructions, intercept.JudgeContextBoundary) || strings.Contains(instructions, intercept.JudgeOutputContract) {
			t.Fatalf("default prompt was copied into Jev instructions: %s", instructions)
		}
	}
}

func TestBuildRequestBoundsLargeEvidence(t *testing.T) {
	args := json.RawMessage(`{"payload":"` + strings.Repeat("x", 50000) + `"}`)
	req, err := BuildRequest(intercept.ReviewInput{Version: 5, Tool: "Bash", Arguments: args}, strings.Repeat("policy ", 3000), "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(req.State)
	if len(b) > maxStateBytes+500 {
		t.Fatalf("bounded state size=%d", len(b))
	}
	if !req.EvidenceTruncated || !strings.Contains(string(b), `"evidence_truncated":true`) {
		t.Fatalf("large evidence was not marked truncated: %s", b[:min(len(b), 200)])
	}
	if !json.Valid(b) || len(b) > maxStateBytes {
		t.Fatalf("truncated evidence state invalid or too large: %d", len(b))
	}
}

func TestTruncateUTF8KeepsBoundary(t *testing.T) {
	got, truncated := truncateUTF8(strings.Repeat("审查", 20), 20)
	if !truncated || !utf8.ValidString(got) || len(got) > 20 {
		t.Fatalf("truncateUTF8 result=%q truncated=%v len=%d", got, truncated, len(got))
	}
}

func TestDecisionFromResponseLowConfidenceForcesAsk(t *testing.T) {
	d, err := DecisionFromResponse(Response{Answers: map[string]Answer{
		"decision": {Choice: "allow", Confidence: 0.3, Probabilities: map[string]float64{"allow": 0.8, "ask": 0.1, "deny": 0.1}},
	}}, 0.55, 42)
	if err != nil {
		t.Fatal(err)
	}
	if d.Action != "ask" || d.ProfileID != 42 || !strings.Contains(d.Message, "置信度") {
		t.Fatalf("unexpected decision: %#v", d)
	}
}

func TestDecisionFromResponseRejectsMissingOrInvalidAnswer(t *testing.T) {
	for _, resp := range []Response{
		{Answers: map[string]Answer{}},
		{Answers: map[string]Answer{"decision": {Choice: "maybe", Confidence: 1}}},
	} {
		if _, err := DecisionFromResponse(resp, 0.55, 1); err == nil {
			t.Fatalf("expected invalid response error for %#v", resp)
		}
	}
}

func TestDecisionFromResponseThresholdZeroAllowsLowConfidence(t *testing.T) {
	d, err := DecisionFromResponse(Response{Answers: map[string]Answer{
		"decision": {Choice: "allow", Confidence: 0.01},
	}}, 0, 1)
	if err != nil || d.Action != "allow" {
		t.Fatalf("threshold zero decision=%#v err=%v", d, err)
	}
}

func TestReviewForcesAskWhenEvidenceWasTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"decision":{"choice":"allow","confidence":1}}}`))
	}))
	defer srv.Close()
	args := json.RawMessage(`{"payload":"` + strings.Repeat("x", 50000) + `"}`)
	d, _, err := (Config{BaseURL: srv.URL, APIKey: "secret", Client: srv.Client()}).ReviewWithUsage(
		context.Background(), intercept.ReviewInput{Version: 5, Tool: "Bash", Arguments: args}, "", 0, 8)
	if err != nil || d.Action != "ask" || !strings.Contains(d.Message, "证据已截断") {
		t.Fatalf("decision=%#v err=%v", d, err)
	}
}

func TestReviewLowConfidenceTruncatedEvidenceKeepsBothReasons(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"decision":{"choice":"allow","confidence":0.2,"probabilities":{"allow":0.8,"ask":0.1,"deny":0.1}}}}`))
	}))
	defer srv.Close()
	args := json.RawMessage(`{"payload":"` + strings.Repeat("x", 50000) + `"}`)
	d, _, err := (Config{BaseURL: srv.URL, APIKey: "secret", Client: srv.Client()}).ReviewWithUsage(
		context.Background(), intercept.ReviewInput{Version: 5, Tool: "Bash", Arguments: args}, "", 0.55, 8)
	if err != nil || d.Action != "ask" || !strings.Contains(d.Message, "置信度") || !strings.Contains(d.Message, "证据已截断") {
		t.Fatalf("decision=%#v err=%v", d, err)
	}
}

func TestReviewForcesAskForIncompleteScriptEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"decision":{"choice":"allow","confidence":1}}}`))
	}))
	defer srv.Close()
	d, _, err := (Config{BaseURL: srv.URL, APIKey: "secret", Client: srv.Client()}).ReviewWithUsage(
		context.Background(), intercept.ReviewInput{Version: 5, Tool: "Bash", ScriptCaptureStatus: "incomplete", Arguments: json.RawMessage(`{"cmd":"run"}`)}, "", 0, 8)
	if err != nil || d.Action != "ask" || !strings.Contains(d.Message, "证据已截断") {
		t.Fatalf("decision=%#v err=%v", d, err)
	}
}

func TestReviewWithUsageKeepsParseableErrorUsage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"usage":{"input_tokens":17,"output_tokens":3},"error":"busy"}`))
	}))
	defer srv.Close()
	_, usage, err := (Config{BaseURL: srv.URL, APIKey: "secret", Client: srv.Client()}).ReviewWithUsage(
		context.Background(), intercept.ReviewInput{Version: 5, Tool: "Read", Arguments: json.RawMessage(`{}`)}, "", 0.55, 3)
	if err == nil || usage.InputTokens != 17 || usage.OutputTokens != 3 {
		t.Fatalf("usage=%+v err=%v", usage, err)
	}
}

func TestSystemOneUsesConfiguredProxy(t *testing.T) {
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("proxy path=%s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"decision":{"choice":"deny","confidence":1}}}`))
	}))
	defer proxy.Close()
	lat, _, err := (Config{BaseURL: "http://target.invalid", Proxy: proxy.URL, APIKey: "secret"}).TestConnection(context.Background())
	if err != nil || lat <= 0 {
		t.Fatalf("proxy test latency=%v err=%v", lat, err)
	}
}

func TestTestConnectionAddsThirtySecondDeadline(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		deadline, ok := req.Context().Deadline()
		if !ok || time.Until(deadline) > 30*time.Second || time.Until(deadline) < 29*time.Second {
			return nil, fmt.Errorf("unexpected deadline: %v", deadline)
		}
		return nil, context.DeadlineExceeded
	})}
	_, _, err := (Config{BaseURL: "http://target.invalid", APIKey: "secret", Client: client}).TestConnection(context.Background())
	if err == nil || !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Fatalf("expected context deadline error, got %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestSystemOneReviewAndConnectionTest(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/systemone" {
			t.Errorf("path=%s", r.URL.Path)
		}
		gotAuth = r.Header.Get("Authorization")
		var req Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode: %v", err)
		}
		if req.Questions["decision"].Criteria["deny"] == "" {
			t.Error("missing deny criterion")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answers":{"decision":{"type":"choice","choice":"deny","confidence":0.91,"probabilities":{"allow":0.02,"ask":0.07,"deny":0.91}}}}`))
	}))
	defer srv.Close()
	c := Config{BaseURL: srv.URL, APIKey: "secret", Model: "jev-latest", Client: srv.Client()}
	d, err := c.Review(context.Background(), intercept.ReviewInput{Version: 5, Tool: "Write", Arguments: json.RawMessage(`{}`)}, "policy", 0.55, 9)
	if err != nil || d.Action != "deny" {
		t.Fatalf("review decision=%#v err=%v", d, err)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("auth=%q", gotAuth)
	}
	if _, _, err := c.TestConnection(context.Background()); err != nil {
		t.Fatalf("connection test: %v", err)
	}
}
