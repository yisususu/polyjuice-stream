package httpapi_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yisususu/polyjuice-stream/src/config"
	"github.com/yisususu/polyjuice-stream/src/httpapi"
)

func testConfig() config.Config {
	cfg := config.Default()
	cfg.Model = "test-model"
	cfg.Chunks = 3
	cfg.ChunkChars = 5
	cfg.FirstDelay = 0
	cfg.Interval = 0
	seed := int64(17)
	cfg.Seed = &seed
	return cfg
}

func handler(t *testing.T, cfg config.Config) http.Handler {
	t.Helper()
	h, err := httpapi.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func requestBody(stream bool) map[string]any {
	return map[string]any{"model": "test-model", "messages": []map[string]string{{"role": "user", "content": "Ignored prompt"}}, "stream": stream}
}

func encoded(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(t *testing.T, h http.Handler, value any) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(encoded(t, value))))
	return w
}

type response struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int               `json:"index"`
		Delta   map[string]string `json:"delta"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Usage json.RawMessage `json:"usage"`
}

func parseJSON(t *testing.T, data []byte) response {
	t.Helper()
	var got response
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("decode response: %v; body=%s", err, data)
	}
	if got.ID == "" || got.Created <= 0 || got.Model != "test-model" || len(got.Choices) != 1 {
		t.Fatalf("invalid envelope: %+v", got)
	}
	return got
}

func readEvent(t *testing.T, reader *bufio.Reader) []byte {
	t.Helper()
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "data: ") {
		t.Fatalf("invalid SSE data line %q", line)
	}
	blank, err := reader.ReadString('\n')
	if err != nil || blank != "\n" {
		t.Fatalf("missing SSE event separator: %q, %v", blank, err)
	}
	return []byte(strings.TrimSuffix(strings.TrimPrefix(line, "data: "), "\n"))
}

func TestStreamArrivesIncrementallyWithStableMetadata(t *testing.T) {
	cfg := testConfig()
	cfg.FirstDelay = 10 * time.Millisecond
	cfg.Interval = 150 * time.Millisecond
	h := handler(t, cfg)
	completed := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(completed)
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
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream" || res.Header.Get("Cache-Control") != "no-cache" || res.Header.Get("Content-Length") != "" {
		t.Fatalf("unexpected streaming response: %d %v", res.StatusCode, res.Header)
	}
	reader := bufio.NewReader(res.Body)
	first := parseJSON(t, readEvent(t, reader))
	select {
	case <-completed:
		t.Fatal("first content was buffered until completion")
	default:
	}
	for i := 0; i < cfg.Chunks; i++ {
		got := first
		if i != 0 {
			got = parseJSON(t, readEvent(t, reader))
		}
		if got.ID != first.ID || got.Created != first.Created || got.Model != first.Model || got.Object != "chat.completion.chunk" {
			t.Fatalf("unstable event envelope: %+v", got)
		}
		choice := got.Choices[0]
		if len(choice.Delta["content"]) != cfg.ChunkChars || choice.FinishReason != nil || got.Usage != nil {
			t.Fatalf("invalid content event: %+v", got)
		}
		if (i == 0 && choice.Delta["role"] != "assistant") || (i != 0 && choice.Delta["role"] != "") {
			t.Fatalf("role must appear only with first content: %+v", choice)
		}
	}
	final := parseJSON(t, readEvent(t, reader))
	if final.ID != first.ID || final.Created != first.Created || len(final.Choices[0].Delta) != 0 || final.Choices[0].FinishReason == nil || *final.Choices[0].FinishReason != "stop" {
		t.Fatalf("invalid finish event: %+v", final)
	}
	if got := string(readEvent(t, reader)); got != "[DONE]" {
		t.Fatalf("missing DONE: %s", got)
	}
	if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
		t.Fatalf("unexpected trailing output: %s, %v", rest, err)
	}
}

func TestStreamingAndCompleteResponsesShareSeededContentAndLimits(t *testing.T) {
	for _, tc := range []struct {
		name       string
		limit      int
		wantChunks int
		reason     string
	}{
		{"uncapped", 0, 3, "stop"}, {"truncated", 2, 2, "length"}, {"equal", 3, 3, "stop"}, {"larger", 8, 3, "stop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testConfig()
			streamHandler, completeHandler := handler(t, cfg), handler(t, cfg)
			for sequence := 0; sequence < 2; sequence++ {
				streamBody, fullBody := requestBody(true), requestBody(false)
				if tc.limit > 0 {
					streamBody["max_tokens"], fullBody["max_completion_tokens"] = tc.limit, tc.limit
				}
				stream := post(t, streamHandler, streamBody)
				complete := post(t, completeHandler, fullBody)
				if stream.Code != 200 || complete.Code != 200 {
					t.Fatalf("request failed: %s / %s", stream.Body, complete.Body)
				}
				var content strings.Builder
				reader := bufio.NewReader(stream.Body)
				for i := 0; i < tc.wantChunks; i++ {
					content.WriteString(parseJSON(t, readEvent(t, reader)).Choices[0].Delta["content"])
				}
				finish := parseJSON(t, readEvent(t, reader))
				full := parseJSON(t, complete.Body.Bytes())
				if content.String() != full.Choices[0].Message.Content || len(content.String()) != tc.wantChunks*cfg.ChunkChars || full.Choices[0].Message.Role != "assistant" || full.Object != "chat.completion" {
					t.Fatalf("stream/full mismatch: %q / %+v", content.String(), full)
				}
				if finish.Choices[0].FinishReason == nil || *finish.Choices[0].FinishReason != tc.reason || full.Choices[0].FinishReason == nil || *full.Choices[0].FinishReason != tc.reason {
					t.Fatalf("wrong finish reason: %+v / %+v", finish, full)
				}
			}
		})
	}
}

func TestGenerationWaitsOutliveWriteTimeout(t *testing.T) {
	for _, stream := range []bool{true, false} {
		cfg := testConfig()
		cfg.Chunks = 2
		cfg.FirstDelay = 200 * time.Millisecond
		cfg.Interval = 200 * time.Millisecond
		cfg.WriteTimeout = 100 * time.Millisecond
		server := httptest.NewServer(handler(t, cfg))
		client := server.Client()
		client.Timeout = 3 * time.Second
		res, err := client.Post(server.URL+"/v1/chat/completions", "application/json", bytes.NewReader(encoded(t, requestBody(stream))))
		if err != nil {
			server.Close()
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		server.Close()
		if err != nil || res.StatusCode != 200 {
			t.Fatalf("generation wait was treated as a write timeout: status=%d body=%s error=%v", res.StatusCode, body, err)
		}
		if stream && !bytes.Contains(body, []byte("data: [DONE]\n\n")) {
			t.Fatalf("stream ended during a generation wait: %s", body)
		}
		if !stream {
			parseJSON(t, body)
		}
	}
}

func TestInvalidRequestsReturnJSONBeforeStreaming(t *testing.T) {
	h := handler(t, testConfig())
	tests := []struct {
		name, field string
		value       any
		remove      bool
		status      int
	}{
		{"missing model", "model", nil, true, 400},
		{"null model", "model", nil, false, 400},
		{"unknown model", "model", "missing", false, 404},
		{"model type", "model", 3, false, 400},
		{"missing messages", "messages", nil, true, 400},
		{"null messages", "messages", nil, false, 400},
		{"empty messages", "messages", []any{}, false, 400},
		{"multimodal", "messages", []any{map[string]any{"role": "user", "content": []any{map[string]string{"type": "text", "text": "hi"}}}}, false, 400},
		{"null content", "messages", []any{map[string]any{"role": "user", "content": nil}}, false, 400},
		{"tool role", "messages", []any{map[string]any{"role": "tool", "content": "hi"}}, false, 400},
		{"assistant tool", "messages", []any{map[string]any{"role": "assistant", "content": "hi", "tool_calls": []any{}}}, false, 400},
		{"null message", "messages", []any{nil}, false, 400},
		{"null stream", "stream", nil, false, 400},
		{"string stream", "stream", "true", false, 400},
		{"zero max", "max_tokens", 0, false, 400},
		{"fraction max", "max_tokens", 1.1, false, 400},
		{"string max", "max_completion_tokens", "5", false, 400},
		{"null max", "max_tokens", nil, false, 400},
		{"many choices", "n", 2, false, 400},
		{"null choices", "n", nil, false, 400},
		{"tools", "tools", []any{}, false, 400},
		{"stop", "stop", "end", false, 400},
		{"structured output", "response_format", map[string]string{"type": "json_object"}, false, 400},
		{"sampling type", "temperature", "0.7", false, 400},
		{"null sampling", "top_p", nil, false, 400},
		{"stream option type", "stream_options", true, false, 400},
		{"usage type", "stream_options", map[string]any{"include_usage": 1}, false, 400},
		{"other stream option", "stream_options", map[string]any{"unknown": true}, false, 400},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body := requestBody(true)
			if tc.remove {
				delete(body, tc.field)
			} else {
				body[tc.field] = tc.value
			}
			assertJSONError(t, post(t, h, body), tc.status)
		})
	}
	body := requestBody(true)
	body["max_tokens"], body["max_completion_tokens"] = 1, 2
	assertJSONError(t, post(t, h, body), 400)
	for _, body := range []string{"{", "null", "[]", `{"model":"test-model"} {}`, string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'})} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))
		assertJSONError(t, w, 400)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(strings.Repeat(" ", httpapi.MaxRequestBytes+1))))
	assertJSONError(t, w, 413)
}

func assertJSONError(t *testing.T, w *httptest.ResponseRecorder, status int) {
	t.Helper()
	var body struct {
		Error struct{ Message, Type, Code string }
	}
	if w.Code != status || w.Header().Get("Content-Type") != "application/json" || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Error.Message == "" || body.Error.Type == "" || body.Error.Code == "" || strings.Contains(w.Body.String(), "data:") {
		t.Fatalf("expected JSON error %d, got %d %v %s", status, w.Code, w.Header(), w.Body)
	}
}

func TestIgnoredExtensionsAndEqualLimits(t *testing.T) {
	h := handler(t, testConfig())
	body := requestBody(false)
	delete(body, "stream")
	body["user"] = map[string]any{"arbitrary": "extension"}
	body["temperature"], body["top_p"] = 0.2, 0.9
	body["presence_penalty"], body["frequency_penalty"], body["seed"] = -0.5, 1, 11
	body["max_tokens"], body["max_completion_tokens"], body["n"] = 2, 2, 1
	body["stream_options"] = map[string]bool{"include_usage": true}
	w := post(t, h, body)
	if w.Code != 200 {
		t.Fatalf("unexpected error: %s", w.Body)
	}
	got := parseJSON(t, w.Body.Bytes())
	if got.Object != "chat.completion" || got.Usage != nil {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestDiscoveryAndMethods(t *testing.T) {
	h := handler(t, testConfig())
	for _, path := range []string{"/health", "/v1/models"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if path == "/v1/models" && !strings.Contains(w.Body.String(), `"id":"test-model"`) {
			t.Fatal(w.Body)
		}
		w = httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		assertJSONError(t, w, 405)
		if w.Header().Get("Allow") != "GET" {
			t.Fatal("missing Allow")
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/chat/completions", nil))
	assertJSONError(t, w, 405)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/missing", nil))
	assertJSONError(t, w, 404)
}

func TestConcurrentRequests(t *testing.T) {
	h := handler(t, testConfig())
	body := encoded(t, requestBody(false))
	var wg sync.WaitGroup
	ids := make(chan string, 32)
	for range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := httptest.NewRecorder()
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body)))
			var got response
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &got) != nil || got.ID == "" {
				t.Errorf("concurrent response: %d %s", w.Code, w.Body)
				return
			}
			ids <- got.ID
		}()
	}
	wg.Wait()
	close(ids)
	seen := make(map[string]bool)
	for id := range ids {
		if seen[id] {
			t.Errorf("duplicate response ID %s", id)
		}
		seen[id] = true
	}
	if len(seen) != 32 {
		t.Fatalf("only %d successful concurrent responses", len(seen))
	}
}

type observedWriter struct {
	*httptest.ResponseRecorder
	firstFlush chan struct{}
	flushErr   error
	writeErr   error
	writes     int
	deadlines  []time.Time
	operations []string
}

func (w *observedWriter) Write(data []byte) (int, error) {
	w.operations = append(w.operations, "write")
	w.writes++
	if w.writeErr != nil {
		return 0, w.writeErr
	}
	return w.ResponseRecorder.Write(data)
}

func (w *observedWriter) FlushError() error {
	w.operations = append(w.operations, "flush")
	if w.firstFlush != nil {
		close(w.firstFlush)
		w.firstFlush = nil
	}
	return w.flushErr
}

func (w *observedWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	if deadline.IsZero() {
		w.operations = append(w.operations, "clear")
	} else {
		w.operations = append(w.operations, "deadline")
	}
	return nil
}

func TestCompleteResponseFlushUsesWriteDeadline(t *testing.T) {
	for _, failure := range []string{"none", "write", "flush"} {
		t.Run(failure, func(t *testing.T) {
			w := &observedWriter{ResponseRecorder: httptest.NewRecorder()}
			want := "deadline,write,flush,clear"
			if failure == "write" {
				w.writeErr = errors.New("write failed")
				want = "deadline,write,clear"
			}
			if failure == "flush" {
				w.flushErr = errors.New("flush failed")
			}
			h := handler(t, testConfig())
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(encoded(t, requestBody(false)))))
			if got := strings.Join(w.operations, ","); got != want {
				t.Fatalf("transport order = %s, want %s", got, want)
			}
			if w.writes != 1 {
				t.Fatalf("attempted another response after transport failure: %d writes", w.writes)
			}
		})
	}
}

func TestCancelStopsWaitingWithoutFinish(t *testing.T) {
	cfg := testConfig()
	cfg.Interval = time.Hour
	h := handler(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstFlush := make(chan struct{})
	w := &observedWriter{ResponseRecorder: httptest.NewRecorder(), firstFlush: firstFlush}
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(encoded(t, requestBody(true)))).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(w, r) }()
	select {
	case <-firstFlush:
	case <-time.After(2 * time.Second):
		t.Fatal("no first chunk")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled request kept waiting")
	}
	if w.writes != 1 || strings.Contains(w.Body.String(), "[DONE]") || strings.Contains(w.Body.String(), `"finish_reason":"`) {
		t.Fatalf("cancellation sent completion: %s", w.Body)
	}
	if len(w.deadlines) != 2 || w.deadlines[0].IsZero() || !w.deadlines[1].IsZero() {
		t.Fatalf("write deadline must be cleared before waiting: %v", w.deadlines)
	}
}

func TestCancellationBeforeFirstContentEmitsNothing(t *testing.T) {
	for _, stream := range []bool{false, true} {
		cfg := testConfig()
		cfg.FirstDelay = time.Hour
		h := handler(t, cfg)
		ctx, cancel := context.WithCancel(context.Background())
		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(encoded(t, requestBody(stream)))).WithContext(ctx)
		done := make(chan struct{})
		go func() { defer close(done); h.ServeHTTP(w, r) }()
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("first-delay wait did not cancel")
		}
		if w.Body.Len() != 0 || w.Flushed {
			t.Fatalf("sent output before first content: %s", w.Body)
		}
	}
}

func TestClientDisconnectCancelsHTTPGeneration(t *testing.T) {
	cfg := testConfig()
	cfg.Interval = time.Hour
	h := handler(t, cfg)
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
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
	if first.Choices[0].Delta["content"] == "" {
		t.Fatal("no first content")
	}
	if err := res.Body.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		server.CloseClientConnections()
		t.Fatal("closing the client stream did not cancel the generation wait")
	}
}

func TestTransportErrorsStopImmediately(t *testing.T) {
	for _, where := range []string{"write", "flush"} {
		t.Run(where, func(t *testing.T) {
			w := &observedWriter{ResponseRecorder: httptest.NewRecorder()}
			if where == "write" {
				w.writeErr = errors.New("connection closed")
			} else {
				w.flushErr = errors.New("flush failed")
			}
			h := handler(t, testConfig())
			h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(encoded(t, requestBody(true)))))
			if w.writes != 1 || strings.Contains(w.Body.String(), "[DONE]") {
				t.Fatalf("continued after transport error: writes=%d body=%s", w.writes, w.Body)
			}
		})
	}
}

type noFlushWriter struct{ http.ResponseWriter }
type unwrappedWriter struct{ http.ResponseWriter }

func (w unwrappedWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestResponseWriterCapabilities(t *testing.T) {
	h := handler(t, testConfig())
	w := httptest.NewRecorder()
	h.ServeHTTP(noFlushWriter{w}, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(encoded(t, requestBody(true)))))
	assertJSONError(t, w, 500)
	w = httptest.NewRecorder()
	h.ServeHTTP(unwrappedWriter{w}, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(encoded(t, requestBody(true)))))
	if w.Code != 200 || !strings.Contains(w.Body.String(), "[DONE]") || !w.Flushed {
		t.Fatalf("unwrapped writer failed: %d %s", w.Code, w.Body)
	}
}
