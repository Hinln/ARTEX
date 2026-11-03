package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJevModelListAndConnectionEndpoints(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Error("missing upstream authorization")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/models":
			if r.Method != http.MethodGet {
				t.Error("models must use GET")
			}
			_, _ = w.Write([]byte(`{"models":[{"name":"jev-latest"},{"name":"jev-preview"}]}`))
		case "/v1/systemone":
			var input map[string]any
			if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&input) != nil || input["model"] != "jev-latest" {
				t.Error("invalid System One request")
			}
			_, _ = w.Write([]byte(`{"model":"jev-latest","answers":{"decision":{"type":"choice","choice":"allow","confidence":0.95,"probabilities":{"allow":0.95,"ask":0.04,"deny":0.01}}},"usage":{"input_tokens":42,"output_tokens":3}}`))
		default:
			t.Errorf("unexpected URL %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	s := &Server{}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
	}{
		{"models", s.pgListModels}, {"connection", s.testLLM},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, _ := json.Marshal(map[string]any{"provider": "jev", "base_url": upstream.URL + "/v1/systemone", "api_key": "test-key"})
			w := httptest.NewRecorder()
			tc.handler(w, httptest.NewRequest(http.MethodPost, "/api/llm/"+tc.name, strings.NewReader(string(payload))))
			var got struct {
				OK     bool     `json:"ok"`
				Models []string `json:"models"`
				Reply  string   `json:"reply"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || w.Code != 200 || !got.OK {
				t.Fatalf("response=%d %s err=%v", w.Code, w.Body, err)
			}
			if tc.name == "models" && strings.Join(got.Models, ",") != "jev-latest,jev-preview" {
				t.Fatalf("models=%v", got.Models)
			}
			if tc.name == "connection" && !strings.Contains(got.Reply, "allow") {
				t.Fatalf("reply=%s", got.Reply)
			}
		})
	}
}
