package gateway

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

func titleTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestTitleCheckV1(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"generate a title for this conversation\nfix the login redirect bug in the dashboard"}]}`)
	if !isTitleGenerationBody(body) {
		t.Fatal("v1 title body not detected")
	}
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	if got := synthesizeTitle(payload); got != "fix the login redirect bug in" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleCheckV2(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"system","content":"You name conversations so the user can find them later in a list of past sessions."},{"role":"user","content":"add retry to the uploader"}]}`)
	if !isTitleGenerationBody(body) {
		t.Fatal("v2 title body not detected")
	}
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	if got := synthesizeTitle(payload); got != "add retry to the uploader" {
		t.Fatalf("title = %q", got)
	}
}

func TestTitleRejectsAgentTraffic(t *testing.T) {
	cases := []string{
		`{"model":"m","messages":[{"role":"user","content":"generate a title for this conversation\nx"}],"tools":[{"type":"function","function":{"name":"bash"}}]}`,
		`{"model":"m","messages":[{"role":"user","content":"generate a title for this conversation\nx"},{"role":"assistant","content":"hi"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"just a normal prompt"}]}`,
		`{"model":"m","messages":[{"role":"user","content":"generate a title for this conversation\nx"},{"role":"user","content":"a"},{"role":"user","content":"b"},{"role":"user","content":"c"}]}`,
	}
	for _, body := range cases {
		if isTitleGenerationBody([]byte(body)) {
			t.Fatalf("agent traffic misclassified: %s", body)
		}
	}
}

func TestTitleSynthesisBounds(t *testing.T) {
	var payload map[string]any
	_ = json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"one two three four five six seven eight"}]}`), &payload)
	title := synthesizeTitle(payload)
	if len(strings.Fields(title)) > titleMaxWords {
		t.Fatalf("too many words: %q", title)
	}
	var empty map[string]any
	_ = json.Unmarshal([]byte(`{"messages":[{"role":"user","content":"   "}]}`), &empty)
	if got := synthesizeTitle(empty); got != defaultTitle {
		t.Fatalf("default title = %q", got)
	}
}

func TestTitleFallbackResponse(t *testing.T) {
	body := []byte(`{"model":"m","messages":[{"role":"user","content":"generate a title for this conversation\nlanding page copy"}],"stream":false}`)
	rejected := &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody}
	fallback := titleFallback(titleTestLogger(), "m", body, rejected, nil)
	if fallback == nil || fallback.StatusCode != http.StatusOK {
		t.Fatal("no fallback for 429")
	}
	var decoded map[string]any
	if err := json.NewDecoder(fallback.Body).Decode(&decoded); err != nil {
		t.Fatal(err)
	}
	choices := decoded["choices"].([]any)
	content := choices[0].(map[string]any)["message"].(map[string]any)["content"]
	if content != "landing page copy" {
		t.Fatalf("fallback content = %v", content)
	}
	streamBody := []byte(`{"messages":[{"role":"user","content":"generate a title for this conversation\nstream me"}],"stream":true}`)
	streamFallback := titleFallback(titleTestLogger(), "m", streamBody, nil, errWARPUnavailable)
	if streamFallback == nil {
		t.Fatal("no fallback for transport failure")
	}
	if ct := streamFallback.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("stream content type = %q", ct)
	}
	if ok := titleFallback(titleTestLogger(), "m", body, &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}, nil); ok != nil {
		t.Fatal("fallback replaced a 200")
	}
}
