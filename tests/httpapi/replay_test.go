package httpapi_test

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/yisususu/polyjuice-stream/src/config"
	"github.com/yisususu/polyjuice-stream/src/httpapi"
)

func replayConfig(t *testing.T, lines string) config.Config {
	t.Helper()
	path := filepath.Join(t.TempDir(), "responses.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Model = "test-model"
	cfg.Dataset = path
	cfg.ChunkChars = 2
	cfg.FirstDelay = 0
	cfg.Interval = 0
	return cfg
}

func replayStreamText(t *testing.T, body io.Reader) (string, string, int) {
	t.Helper()
	reader := bufio.NewReader(body)
	var text strings.Builder
	finish := ""
	count := 0
	for {
		event := readEvent(t, reader)
		if string(event) == "[DONE]" {
			break
		}
		got := parseJSON(t, event)
		choice := got.Choices[0]
		if chunk, exists := choice.Delta["content"]; exists {
			if !utf8.ValidString(chunk) {
				t.Fatalf("invalid UTF-8 chunk: %q", chunk)
			}
			text.WriteString(chunk)
			count++
		}
		if choice.FinishReason != nil {
			finish = *choice.FinishReason
		}
	}
	if finish == "" {
		t.Fatal("stream ended without a finish reason")
	}
	return text.String(), finish, count
}

func TestReplayCyclesAcrossResponseModesAndIgnoresPrompt(t *testing.T) {
	first, second := "你好，世界🙂🚀 e\u0301 👨‍👩‍👧‍👦", "Another response\nwith a new line"
	lines := string(encoded(t, map[string]string{"result": first})) + "\n" + string(encoded(t, map[string]string{"result": second})) + "\n"
	cfg := replayConfig(t, lines)
	h := handler(t, cfg)
	for i, want := range []string{first, second, first, second} {
		stream := i == 0 || i == 3
		body := requestBody(stream)
		body["messages"] = []map[string]string{{"role": "user", "content": fmt.Sprintf("Different prompt %d", i)}}
		w := post(t, h, body)
		if w.Code != http.StatusOK {
			t.Fatalf("request %d failed: %d %s", i, w.Code, w.Body)
		}
		var got, reason string
		if stream {
			var chunks int
			got, reason, chunks = replayStreamText(t, w.Body)
			if chunks != (utf8.RuneCountInString(want)+cfg.ChunkChars-1)/cfg.ChunkChars {
				t.Fatalf("wrong Unicode chunk count: %d", chunks)
			}
		} else {
			full := parseJSON(t, w.Body.Bytes())
			got = full.Choices[0].Message.Content
			if full.Choices[0].FinishReason != nil {
				reason = *full.Choices[0].FinishReason
			}
		}
		if got != want || reason != "stop" {
			t.Fatalf("request %d: got %q (%s), want %q (stop)", i, got, reason, want)
		}
	}
}

func TestReplayLimitsAfterSelectionAndInvalidRequestsDoNotConsume(t *testing.T) {
	cfg := replayConfig(t, "{\"result\":\"你好🙂🚀再见\"}\n{\"result\":\"第二条\"}\n")
	h := handler(t, cfg)
	invalid := requestBody(true)
	invalid["model"] = "missing"
	assertJSONError(t, post(t, h, invalid), http.StatusNotFound)
	invalid = requestBody(true)
	invalid["max_tokens"] = 0
	assertJSONError(t, post(t, h, invalid), http.StatusBadRequest)
	limited := requestBody(true)
	limited["max_completion_tokens"] = 2
	w := post(t, h, limited)
	if w.Code != http.StatusOK {
		t.Fatalf("limited replay failed: %s", w.Body)
	}
	text, reason, chunks := replayStreamText(t, w.Body)
	if text != "你好🙂🚀" || reason != "length" || chunks != 2 {
		t.Fatalf("limited response: %q %s %d", text, reason, chunks)
	}
	full := parseJSON(t, post(t, h, requestBody(false)).Body.Bytes())
	if full.Choices[0].Message.Content != "第二条" {
		t.Fatalf("truncation must consume one whole record: %+v", full)
	}
}

func TestReplayLoadsSelectedFieldOnlyOnceAtStartup(t *testing.T) {
	cfg := replayConfig(t, "{\"output\":\"Loaded once 🙂\",\"result\":\"Not selected\"}\n")
	cfg.TextField = "output"
	h := handler(t, cfg)
	if err := os.Remove(cfg.Dataset); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		w := post(t, h, requestBody(false))
		if w.Code != http.StatusOK || parseJSON(t, w.Body.Bytes()).Choices[0].Message.Content != "Loaded once 🙂" {
			t.Fatalf("request reread the deleted dataset: %d %s", w.Code, w.Body)
		}
	}
}

func TestReplayDisconnectConsumesAssignedRecord(t *testing.T) {
	cfg := replayConfig(t, "{\"result\":\"abcdef\"}\n{\"result\":\"Z\"}\n")
	cfg.Interval = time.Hour
	h := handler(t, cfg)
	completed := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { completed <- struct{}{} }()
		h.ServeHTTP(w, r)
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	res, err := client.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(encoded(t, requestBody(true))))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	first := parseJSON(t, readEvent(t, bufio.NewReader(res.Body)))
	if first.Choices[0].Delta["content"] != "ab" {
		t.Fatalf("first record was not assigned: %+v", first)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-completed:
	case <-time.After(2 * time.Second):
		server.CloseClientConnections()
		t.Fatal("disconnected replay kept waiting")
	}
	res, err = client.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(encoded(t, requestBody(false))))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK || parseJSON(t, data).Choices[0].Message.Content != "Z" {
		t.Fatalf("cancelled record was replayed again: %d %s", res.StatusCode, data)
	}
}

func TestReplayRejectsBadLaterRecordBeforeCreatingHandler(t *testing.T) {
	cfg := replayConfig(t, "{\"result\":\"Valid first record\"}\n\n{\"output\":\"Missing selected field\"}\n")
	h, err := httpapi.New(cfg)
	if err == nil || h != nil || !strings.Contains(err.Error(), cfg.Dataset+":3:") {
		t.Fatalf("bad dataset was not rejected with physical location: handler=%v error=%v", h, err)
	}
}

func TestReplayUsesRecordLengthInsteadOfRandomChunkCount(t *testing.T) {
	text := strings.Repeat("🙂", 201)
	cfg := replayConfig(t, string(encoded(t, map[string]string{"result": text}))+"\n")
	cfg.ChunkChars = 1
	h := handler(t, cfg)
	w := post(t, h, requestBody(true))
	if w.Code != http.StatusOK {
		t.Fatalf("long replay failed: %d %s", w.Code, w.Body)
	}
	got, reason, chunks := replayStreamText(t, w.Body)
	if got != text || reason != "stop" || chunks != 201 {
		t.Fatalf("random chunk default truncated replay: runes=%d reason=%s chunks=%d", utf8.RuneCountInString(got), reason, chunks)
	}
}

func TestReplayValidatesDurationUsingLongestLoadedRecord(t *testing.T) {
	for _, tc := range []struct {
		text  string
		valid bool
	}{
		{"ab", true},
		{"abc", false},
	} {
		cfg := replayConfig(t, "{\"result\":\"a\"}\n"+string(encoded(t, map[string]string{"result": tc.text}))+"\n")
		cfg.ChunkChars = 1
		cfg.Interval = 2000000 * time.Hour
		h, err := httpapi.New(cfg)
		if tc.valid {
			if err != nil || h == nil {
				t.Fatalf("valid loaded-record duration rejected: %v", err)
			}
		} else if err == nil || h != nil || !strings.Contains(err.Error(), "overflows") {
			t.Fatalf("longest loaded record duration not checked: handler=%v error=%v", h, err)
		}
	}
}
