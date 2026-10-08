package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

type updateTransportFunc func(*http.Request) (*http.Response, error)

func (f updateTransportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func updateResponse(req *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: req}
}

func TestPrivateReleaseMetadataChecksumsAndDownload(t *testing.T) {
	t.Setenv(GitHubTokenEnv, "test-private-release-token")
	payload := "release archive bytes"
	sum := sha256.Sum256([]byte(payload))
	wantSum := hex.EncodeToString(sum[:])
	client := NewClient("")
	seen := map[string]bool{}
	client.Transport = updateTransportFunc(func(req *http.Request) (*http.Response, error) {
		seen[req.URL.String()] = true
		if req.URL.Host == "api.github.com" {
			if req.Header.Get("Authorization") != "Bearer test-private-release-token" {
				t.Fatal("private Release API request missing authentication")
			}
		} else if req.Header.Get("Authorization") != "" {
			t.Fatal("token leaked to release object storage")
		}
		switch req.URL.String() {
		case latestURL:
			if req.Header.Get("Accept") != "application/vnd.github+json" {
				t.Fatal("metadata request must ask for JSON")
			}
			return updateResponse(req, 200, fmt.Sprintf(`{"tag_name":"v0.3.16","assets":[
				{"name":"SHA256SUMS","url":"https://api.github.com/repos/%s/releases/assets/1","browser_download_url":"https://github.com/unusable-sums"},
				{"name":"artex-0.3.16-darwin-arm64.zip","url":"https://api.github.com/repos/%s/releases/assets/2","browser_download_url":"https://github.com/unusable-zip","size":%d}
			]}`, Repo, Repo, len(payload))), nil
		case "https://api.github.com/repos/" + Repo + "/releases/assets/1":
			if req.Header.Get("Accept") != "application/octet-stream" {
				t.Fatal("checksum request must ask for binary data")
			}
			return updateResponse(req, 200, wantSum+"  artex-0.3.16-darwin-arm64.zip\n"), nil
		case "https://api.github.com/repos/" + Repo + "/releases/assets/2":
			if req.Header.Get("Accept") != "application/octet-stream" {
				t.Fatal("archive request must ask for binary data")
			}
			resp := updateResponse(req, 302, "")
			resp.Header.Set("Location", "https://release-assets.githubusercontent.com/signed-archive")
			return resp, nil
		case "https://release-assets.githubusercontent.com/signed-archive":
			return updateResponse(req, 200, payload), nil
		default:
			t.Fatalf("unexpected update URL: %s", req.URL)
			return nil, nil
		}
	})
	rel, err := FetchLatest(t.Context(), client)
	if err != nil {
		t.Fatal(err)
	}
	sums, err := fetchSums(t.Context(), client, rel)
	if err != nil {
		t.Fatal(err)
	}
	asset, ok := rel.FindAsset("artex-0.3.16-darwin-arm64.zip")
	if !ok {
		t.Fatal("archive missing")
	}
	dst := filepath.Join(t.TempDir(), "release.zip")
	got, err := download(t.Context(), client, asset, dst, func(Phase, int, string) {})
	if err != nil {
		t.Fatal(err)
	}
	if got != sums[asset.Name] || got != wantSum || readAll(t, dst) != payload {
		t.Fatal("downloaded archive/checksum mismatch")
	}
	if len(seen) != 4 {
		t.Fatalf("expected metadata, sums, archive API and CDN requests, got %d", len(seen))
	}
}

func TestUpdateTokenNeverLeavesReleaseAPI(t *testing.T) {
	t.Setenv(GitHubTokenEnv, "test-token")
	for _, raw := range []string{
		"https://github.com/" + Repo + "/releases/download/v1/file.zip",
		"https://api.github.com/repos/other/repo/releases/assets/1",
		"https://api.github.com/repos/" + Repo + "-other/releases/assets/1",
		"https://api.github.com:8443/repos/" + Repo + "/releases/assets/1",
		"https://api.github.com/repos/" + Repo + "/releases%2Fassets/1",
		"https://objects.githubusercontent.com/archive",
		"http://api.github.com/repos/" + Repo + "/releases/assets/1",
	} {
		t.Run(raw, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, raw, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Authorization", "Bearer inherited-token")
			setUpdateAuth(req)
			if req.Header.Get("Authorization") != "" {
				t.Fatal("unsafe destination retained token")
			}
		})
	}
	// A same-host redirect must not inherit authorization for a different repo.
	req, _ := http.NewRequest(http.MethodGet, "https://api.github.com/repos/other/repo/releases/assets/1", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	if err := NewClient("").CheckRedirect(req, nil); err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Authorization") != "" {
		t.Fatal("same-host redirect leaked token to another repo")
	}
}

func TestPublicReleaseDownloadWithoutToken(t *testing.T) {
	t.Setenv(GitHubTokenEnv, "")
	asset := Asset{Name: "archive", URL: "https://github.com/" + Repo + "/releases/download/v1/archive.zip"}
	client := NewClient("")
	client.Transport = updateTransportFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != asset.URL || req.Header.Get("Authorization") != "" {
			t.Fatal("invalid public asset request")
		}
		return updateResponse(req, 200, "archive"), nil
	})
	if _, err := download(t.Context(), client, asset, filepath.Join(t.TempDir(), "archive"), func(Phase, int, string) {}); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseAuthenticationErrors(t *testing.T) {
	t.Setenv(GitHubTokenEnv, "")
	for status, message := range map[int]string{401: GitHubTokenEnv, 403: "Contents", 404: Repo, 429: "限流"} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			client := &http.Client{Transport: updateTransportFunc(func(req *http.Request) (*http.Response, error) { return updateResponse(req, status, "{}"), nil })}
			_, err := FetchLatest(t.Context(), client)
			if err == nil || !strings.Contains(err.Error(), message) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
