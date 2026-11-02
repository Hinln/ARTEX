package intercept

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

const reviewTextLimit = 4000
const maxReviewScriptBytes = 1 << 20

const BackgroundUserMessage = "user_message"

// ReviewBackground is explicitly bound from the current human message. Generated
// Worker summaries are not accepted. Background cannot override review policy.
type ReviewBackground struct {
	Source    string `json:"source"`
	Text      string `json:"text"`
	Truncated bool   `json:"truncated,omitempty"`
}

// ReviewScript is a bounded immutable snapshot of a script referenced by the
// current tool call. The reviewer has no file tool or execution capability;
// this snapshot is the only source-code evidence it can inspect.
type ReviewScript struct {
	Path      string `json:"path"`
	Source    string `json:"source,omitempty"` // file | command; command files are not created yet
	SHA256    string `json:"sha256,omitempty"`
	Bytes     int64  `json:"bytes,omitempty"`
	Content   string `json:"content,omitempty"`
	Complete  bool   `json:"complete"`
	Truncated bool   `json:"truncated,omitempty"`
	ReadError string `json:"read_error,omitempty"`
}

// ReviewInput contains only the current call and explicitly selected background.
// Execution history and call correlation belong to the separate audit record.
type ReviewInput struct {
	Version             int               `json:"version"`
	WorkingDir          string            `json:"working_directory,omitempty"`
	Background          *ReviewBackground `json:"background,omitempty"`
	ScriptCaptureStatus string            `json:"script_capture_status,omitempty"`
	Scripts             []ReviewScript    `json:"script_files,omitempty"`
	Tool                string            `json:"tool_name"`
	Arguments           json.RawMessage   `json:"arguments"`
	EvidenceTruncated   bool              `json:"evidence_truncated,omitempty"`
}

type reviewContextKey struct{}
type reviewEnvironment struct {
	workingDir string
	background ReviewBackground
}

// WithReviewContext explicitly binds the permitted background for one run. Never
// fall back to the raw turn transcript: it may contain the full scheduler prompt.
// This is application wiring, not a model-callable tool.
func WithReviewContext(ctx context.Context, workingDir string, background ReviewBackground) context.Context {
	return context.WithValue(ctx, reviewContextKey{}, reviewEnvironment{workingDir, background})
}

// WithReviewWorkingDirectory preserves only explicitly selected background.
// Chat runs can be human-initiated or scheduled, so the Agent must not infer
// message provenance from the text it receives.
func WithReviewWorkingDirectory(ctx context.Context, workingDir string) context.Context {
	env, _ := ctx.Value(reviewContextKey{}).(reviewEnvironment)
	env.workingDir = workingDir
	return context.WithValue(ctx, reviewContextKey{}, env)
}

func BuildReviewInput(ctx context.Context, tool string, arguments json.RawMessage) (ReviewInput, error) {
	if !json.Valid(arguments) {
		return ReviewInput{}, fmt.Errorf("工具参数不是有效 JSON")
	}
	in := ReviewInput{Version: 5, Tool: tool, Arguments: append(json.RawMessage(nil), arguments...)}
	if env, ok := ctx.Value(reviewContextKey{}).(reviewEnvironment); ok {
		in.WorkingDir = env.workingDir
		background := env.background
		if background.Source == BackgroundUserMessage && strings.TrimSpace(background.Text) != "" {
			var cut bool
			background.Text, cut = bounded(background.Text, reviewTextLimit)
			background.Truncated = background.Truncated || cut
			in.Background = &background
		}
	}
	in.Scripts, in.ScriptCaptureStatus = captureScripts(in.WorkingDir, tool, arguments)
	return in, nil
}
