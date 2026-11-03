package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Autumn-27/norma/permission"
	actool "github.com/Autumn-27/norma/tool"
)

const deepSeekToolsURL = "https://api.deepseek.com/v1/tools/"

func deepSeekTools(opts WebSearchOpts) []actool.CoreTool {
	if !opts.Enabled || opts.Backend != "deepseek" {
		return nil
	}
	key := strings.TrimSpace(opts.DeepSeekAPIKey)
	if key == "" {
		return nil
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if proxy := strings.TrimSpace(opts.Proxy); proxy != "" {
		if parsed, err := url.Parse(proxy); err == nil && parsed.Scheme != "" && parsed.Host != "" {
			client.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
		} else {
			return nil
		}
	}
	return []actool.CoreTool{
		deepSeekTool("deepseek_search", "Search the web with DeepSeek and return ranked titles, URLs, and snippets.", "search", key, client, map[string]any{
			"query": map[string]any{"type": "string", "description": "Search query"},
			"count": map[string]any{"type": "integer", "description": "Maximum results (1-10)"},
		}, "query"),
		deepSeekTool("deepseek_fetch", "Fetch a web page with DeepSeek and return its extracted content.", "fetch", key, client, map[string]any{
			"url": map[string]any{"type": "string", "description": "Page URL to fetch"},
		}, "url"),
	}
}

func DeepSeekSearchProbe(ctx context.Context, key, proxy, query string, count int) (int, error) {
	if strings.TrimSpace(key) == "" {
		return 0, fmt.Errorf("DeepSeek Search API Key is required")
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if strings.TrimSpace(proxy) != "" {
		parsed, err := url.Parse(proxy)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return 0, fmt.Errorf("invalid search proxy URL")
		}
		client.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
	}
	body, _ := json.Marshal(map[string]any{"query": query, "count": count})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, deepSeekToolsURL+"search", strings.NewReader(string(body)))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var result map[string]json.RawMessage
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
		return 0, fmt.Errorf("DeepSeek returned invalid JSON: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, fmt.Errorf("DeepSeek search returned HTTP %d: %s", resp.StatusCode, string(result["error"]))
	}
	for _, field := range []string{"results", "data", "items"} {
		var items []json.RawMessage
		if json.Unmarshal(result[field], &items) == nil && len(items) > 0 {
			return len(items), nil
		}
	}
	return 0, fmt.Errorf("DeepSeek search response did not contain results")
}

func deepSeekTool(name, description, endpoint, key string, client *http.Client, properties map[string]any, required string) actool.CoreTool {
	return actool.Build(actool.Spec{
		Name: name, Description: description,
		Schema:   map[string]any{"type": "object", "properties": properties, "required": []any{required}},
		ReadOnly: func(json.RawMessage) bool { return true }, Concurrent: func(json.RawMessage) bool { return true },
		Permissions: func(context.Context, json.RawMessage, permission.Context) permission.Decision {
			return permission.Allowed()
		},
		Run: func(ctx context.Context, input json.RawMessage, _ *actool.ToolContext) (actool.Result, error) {
			var args map[string]any
			if err := json.Unmarshal(input, &args); err != nil {
				return actool.Errorf("invalid input"), nil
			}
			body, _ := json.Marshal(args)
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, deepSeekToolsURL+endpoint, strings.NewReader(string(body)))
			if err != nil {
				return actool.Errorf(err.Error()), nil
			}
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			resp, err := client.Do(req)
			if err != nil {
				return actool.Errorf("DeepSeek " + endpoint + " failed: " + err.Error()), nil
			}
			defer resp.Body.Close()
			var result json.RawMessage
			if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&result); err != nil {
				return actool.Errorf("DeepSeek returned invalid JSON"), nil
			}
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return actool.Errorf(fmt.Sprintf("DeepSeek %s returned HTTP %d: %s", endpoint, resp.StatusCode, string(result))), nil
			}
			return actool.Text(string(result)), nil
		},
	})
}
