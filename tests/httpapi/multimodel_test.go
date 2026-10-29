package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yisususu/polyjuice-stream/src/config"
)

func replayProfile(t *testing.T, records ...string) config.ModelConfig {
	t.Helper()
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	for _, record := range records {
		if err := encoder.Encode(map[string]string{"result": record}); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join(t.TempDir(), "responses.jsonl")
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	profile := config.DefaultModel()
	profile.Dataset = path
	profile.FirstDelay, profile.Interval = 0, 0
	profile.ChunkChars = 2
	return profile
}

func namedBody(name string, stream bool) map[string]any {
	body := requestBody(stream)
	body["model"] = name
	return body
}

// Read both response formats without assuming a particular model name.
func namedText(w *httptest.ResponseRecorder, model string, stream bool) (string, error) {
	if w.Code != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", w.Code, w.Body)
	}
	decode := func(data []byte) (response, error) {
		var result response
		if err := json.Unmarshal(data, &result); err != nil {
			return result, err
		}
		if result.ID == "" || result.Created <= 0 || result.Model != model || len(result.Choices) != 1 {
			return result, fmt.Errorf("invalid response metadata: %+v", result)
		}
		return result, nil
	}
	if !stream {
		result, err := decode(w.Body.Bytes())
		if err != nil {
			return "", err
		}
		if result.Object != "chat.completion" || result.Choices[0].Message.Role != "assistant" || result.Choices[0].FinishReason == nil {
			return "", fmt.Errorf("invalid complete response: %+v", result)
		}
		return result.Choices[0].Message.Content, nil
	}
	var text strings.Builder
	var id string
	var created int64
	finished, done := false, false
	for _, frame := range strings.Split(w.Body.String(), "\n\n") {
		if frame == "" {
			continue
		}
		if !strings.HasPrefix(frame, "data: ") || done {
			return "", fmt.Errorf("invalid SSE frame %q", frame)
		}
		data := strings.TrimPrefix(frame, "data: ")
		if data == "[DONE]" {
			done = true
			continue
		}
		result, err := decode([]byte(data))
		if err != nil {
			return "", err
		}
		if id == "" {
			id, created = result.ID, result.Created
		}
		if result.ID != id || result.Created != created || result.Object != "chat.completion.chunk" {
			return "", fmt.Errorf("unstable SSE metadata: %+v", result)
		}
		text.WriteString(result.Choices[0].Delta["content"])
		finished = finished || result.Choices[0].FinishReason != nil
	}
	if !done || !finished {
		return "", fmt.Errorf("incomplete SSE response: %s", w.Body)
	}
	return text.String(), nil
}

func TestMultiModelDiscoveryAndUnknownModelDoNotConsumeRecords(t *testing.T) {
	cfg := config.Default()
	cfg.Models = map[string]config.ModelConfig{
		"zeta":  replayProfile(t, "Z1", "Z2"),
		"alpha": replayProfile(t, "A1", "A2"),
	}
	h := handler(t, cfg)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/models", nil))
	var list struct {
		Object string
		Data   []struct{ ID string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != 200 {
		t.Fatalf("discovery: status=%d body=%s error=%v", w.Code, w.Body, err)
	}
	var names []string
	for _, entry := range list.Data {
		names = append(names, entry.ID)
	}
	if list.Object != "list" || !reflect.DeepEqual(names, []string{"alpha", "zeta"}) {
		t.Fatalf("unexpected model discovery: %+v", list)
	}
	w = post(t, h, namedBody("missing", true))
	assertJSONError(t, w, http.StatusNotFound)
	var failure struct{ Error struct{ Code, Param string } }
	if err := json.Unmarshal(w.Body.Bytes(), &failure); err != nil || failure.Error.Code != "model_not_found" || failure.Error.Param != "model" {
		t.Fatalf("unknown model error: %s", w.Body)
	}
	for name, want := range map[string]string{"alpha": "A1", "zeta": "Z1"} {
		got, err := namedText(post(t, h, namedBody(name, false)), name, false)
		if err != nil || got != want {
			t.Fatalf("unknown request consumed %s: got=%q error=%v", name, got, err)
		}
	}
}

func TestMultiModelReplayCursorsRemainIndependentForTheSameFile(t *testing.T) {
	alpha := replayProfile(t, "第一条🙂", "第二条🚀")
	beta := alpha
	beta.ChunkChars = 1
	cfg := config.Default()
	cfg.Models = map[string]config.ModelConfig{"alpha": alpha, "beta": beta}
	h := handler(t, cfg)
	for i, tc := range []struct{ model, text string }{
		{"alpha", "第一条🙂"}, {"alpha", "第二条🚀"},
		{"beta", "第一条🙂"}, {"beta", "第二条🚀"},
		{"alpha", "第一条🙂"}, {"beta", "第一条🙂"},
	} {
		stream := i%2 == 0
		got, err := namedText(post(t, h, namedBody(tc.model, stream)), tc.model, stream)
		if err != nil || got != tc.text {
			t.Fatalf("request %d to %s: got=%q want=%q error=%v", i, tc.model, got, tc.text, err)
		}
	}
}

type modelResult struct {
	model string
	text  string
	err   error
}

func concurrentModelResults(t *testing.T, h http.Handler, names []string, count int) map[string]map[string]int {
	t.Helper()
	results := make(chan modelResult, len(names)*count)
	start := make(chan struct{})
	var workers sync.WaitGroup
	for _, name := range names {
		for i := range count {
			stream := i%2 == 0
			data := encoded(t, namedBody(name, stream))
			workers.Add(1)
			go func() {
				defer workers.Done()
				<-start
				w := httptest.NewRecorder()
				h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(data)))
				text, err := namedText(w, name, stream)
				results <- modelResult{name, text, err}
			}()
		}
	}
	close(start)
	workers.Wait()
	close(results)
	counts := make(map[string]map[string]int)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent %s request: %v", result.model, result.err)
		}
		if counts[result.model] == nil {
			counts[result.model] = make(map[string]int)
		}
		counts[result.model][result.text]++
	}
	return counts
}

func TestMultiModelConcurrentReplayDoesNotMixContentOrCursors(t *testing.T) {
	cfg := config.Default()
	cfg.Models = map[string]config.ModelConfig{
		"alpha": replayProfile(t, "A1🙂", "A2🙂", "A3🙂"),
		"beta":  replayProfile(t, "B1🚀", "B2🚀"),
	}
	got := concurrentModelResults(t, handler(t, cfg), []string{"alpha", "beta"}, 60)
	want := map[string]map[string]int{
		"alpha": {"A1🙂": 20, "A2🙂": 20, "A3🙂": 20},
		"beta":  {"B1🚀": 30, "B2🚀": 30},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("concurrent replay crossed model state: got=%v want=%v", got, want)
	}
}

func TestMultiModelRandomSequencesMatchIndependentServers(t *testing.T) {
	seed := int64(42)
	alpha := config.DefaultModel()
	alpha.Seed, alpha.Chunks, alpha.ChunkChars = &seed, 3, 7
	alpha.FirstDelay, alpha.Interval = 0, 0
	beta := alpha
	beta.Chunks, beta.ChunkChars = 5, 4
	cfg := config.Default()
	cfg.Models = map[string]config.ModelConfig{"alpha": alpha, "beta": beta}
	baselines := make(map[string]http.Handler)
	want := make(map[string]map[string]int)
	const count = 32
	for name, profile := range cfg.Models {
		single := config.Default()
		single.Model, single.ModelConfig = name, profile
		baselines[name] = handler(t, single)
		want[name] = make(map[string]int)
		for range count {
			text, err := namedText(post(t, baselines[name], namedBody(name, false)), name, false)
			if err != nil {
				t.Fatal(err)
			}
			want[name][text]++
		}
	}
	h := handler(t, cfg)
	got := concurrentModelResults(t, h, []string{"alpha", "beta"}, count)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("random sequences are not independent: got=%v want=%v", got, want)
	}
	for _, name := range []string{"beta", "alpha", "alpha", "beta"} {
		actual, err := namedText(post(t, h, namedBody(name, false)), name, false)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := namedText(post(t, baselines[name], namedBody(name, false)), name, false)
		if err != nil || actual != expected {
			t.Fatalf("interleaved %s sequence changed: got=%q want=%q error=%v", name, actual, expected, err)
		}
	}
}

type gatedModelWriter struct {
	*httptest.ResponseRecorder
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *gatedModelWriter) FlushError() error {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	return nil
}

func TestMultiModelBlockedFlushDoesNotHoldRequestLocks(t *testing.T) {
	profile := replayProfile(t, "record one", "record two", "record three")
	cfg := config.Default()
	cfg.Models = map[string]config.ModelConfig{"alpha": profile, "beta": profile}
	h := handler(t, cfg)
	w := &gatedModelWriter{ResponseRecorder: httptest.NewRecorder(), entered: make(chan struct{}), release: make(chan struct{})}
	request := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(encoded(t, namedBody("alpha", true))))
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(w, request) }()
	defer func() { close(w.release); <-done }()
	select {
	case <-w.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first stream did not reach its flush")
	}
	for _, tc := range []struct{ model, text string }{{"beta", "record one"}, {"alpha", "record two"}} {
		result := make(chan modelResult, 1)
		data := encoded(t, namedBody(tc.model, false))
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		go func() {
			response := httptest.NewRecorder()
			h.ServeHTTP(response, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(data)).WithContext(ctx))
			text, err := namedText(response, tc.model, false)
			result <- modelResult{tc.model, text, err}
		}()
		select {
		case got := <-result:
			if got.err != nil || got.text != tc.text {
				t.Fatalf("request while alpha blocked: %+v, want %q", got, tc.text)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("blocked network flush held %s request", tc.model)
		}
	}
}

func TestMultiModelSlowIntervalDoesNotDelayFastModel(t *testing.T) {
	slow := replayProfile(t, "slow response")
	slow.Interval = time.Hour
	fast := replayProfile(t, "fast response")
	cfg := config.Default()
	cfg.Models = map[string]config.ModelConfig{"slow": slow, "fast": fast}
	h := handler(t, cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	flushed := make(chan struct{})
	w := &observedWriter{ResponseRecorder: httptest.NewRecorder(), firstFlush: flushed}
	r := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(encoded(t, namedBody("slow", true)))).WithContext(ctx)
	done := make(chan struct{})
	go func() { defer close(done); h.ServeHTTP(w, r) }()
	defer func() { cancel(); <-done }()
	select {
	case <-flushed:
	case <-time.After(3 * time.Second):
		t.Fatal("slow model did not emit its first chunk")
	}
	result := make(chan modelResult, 1)
	data := encoded(t, namedBody("fast", false))
	fastContext, stopFast := context.WithTimeout(context.Background(), 3*time.Second)
	defer stopFast()
	go func() {
		response := httptest.NewRecorder()
		h.ServeHTTP(response, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(data)).WithContext(fastContext))
		text, err := namedText(response, "fast", false)
		result <- modelResult{"fast", text, err}
	}()
	select {
	case got := <-result:
		if got.err != nil || got.text != "fast response" {
			t.Fatalf("fast model used slow configuration: %+v", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("slow model's interval blocked fast model")
	}
	select {
	case <-done:
		t.Fatal("slow model did not preserve its independent interval")
	default:
	}
}

func TestMultiModelRegistrySnapshotsCallerConfiguration(t *testing.T) {
	alpha := replayProfile(t, "original alpha")
	beta := replayProfile(t, "original beta")
	cfg := config.Default()
	cfg.Models = map[string]config.ModelConfig{"alpha": alpha, "beta": beta}
	h := handler(t, cfg)
	// Callers may reuse their configuration after construction. The live server
	// must retain its own model set and pacing values.
	alpha.FirstDelay, alpha.Interval = time.Hour, time.Hour
	cfg.Models["alpha"] = alpha
	delete(cfg.Models, "beta")
	cfg.Models["replacement"] = beta
	for name, expected := range map[string]string{"alpha": "original alpha", "beta": "original beta"} {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		data := encoded(t, namedBody(name, false))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(data)).WithContext(ctx))
		cancel()
		actual, err := namedText(w, name, false)
		if err != nil || actual != expected {
			t.Fatalf("caller mutation changed %s: text=%q error=%v", name, actual, err)
		}
	}
	assertJSONError(t, post(t, h, namedBody("replacement", true)), http.StatusNotFound)
}
