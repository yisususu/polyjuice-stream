package source_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/yisususu/polyjuice-stream/src/source"
)

func datasetFile(t *testing.T, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "responses.jsonl")
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func nextReplay(t *testing.T, replay *source.Replay) []string {
	t.Helper()
	chunks, err := replay.Next()
	if err != nil {
		t.Fatal(err)
	}
	return chunks
}

func TestReplayCyclesAndSnapshotsWithoutMutatingRecords(t *testing.T) {
	path := datasetFile(t, "\n{\"result\":\"First\"}\r\n \t\r\n{\"result\":\"Second\"}")
	replay, err := source.LoadJSONL(path, "result", 2)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Len() != 2 || replay.MaxChunks() != 3 {
		t.Fatalf("records=%d max chunks=%d", replay.Len(), replay.MaxChunks())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	first := nextReplay(t, replay)
	if got := strings.Join(first, ""); got != "First" {
		t.Fatal(got)
	}
	first[0] = "changed"
	// Dropping a claimed response, as a cancelled request does, consumes it.
	_ = nextReplay(t, replay)
	for _, want := range []string{"First", "Second", "First"} {
		if got := strings.Join(nextReplay(t, replay), ""); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestReplayPreservesUnicodeWhitespaceAndEscapes(t *testing.T) {
	text := " 你好🙂👩‍💻e\u0301\n\t / \\ud800 "
	encoded, err := json.Marshal(map[string]string{"output.text": text})
	if err != nil {
		t.Fatal(err)
	}
	for _, width := range []int{1, 2, 4, 4096} {
		replay, err := source.LoadJSONL(datasetFile(t, string(encoded)), "output.text", width)
		if err != nil {
			t.Fatal(err)
		}
		chunks := nextReplay(t, replay)
		if strings.Join(chunks, "") != text {
			t.Fatalf("Unicode or whitespace changed: %q", chunks)
		}
		for _, chunk := range chunks {
			if !utf8.ValidString(chunk) || utf8.RuneCountInString(chunk) > width {
				t.Fatalf("invalid chunk %q for width %d", chunk, width)
			}
		}
	}
	replay, err := source.LoadJSONL(datasetFile(t, `{"output":"\u4f60\u597d\ud83d\ude42"}`), "output", 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := nextReplay(t, replay); !reflect.DeepEqual(got, []string{"你好", "🙂"}) {
		t.Fatalf("escaped Unicode: %q", got)
	}
}

func TestReplayRejectsBadDataWithPhysicalLineNumbers(t *testing.T) {
	for _, tc := range []struct {
		name, record, message string
	}{
		{"malformed", `{"result":`, "JSON object"},
		{"trailing JSON", `{"result":"a"}{"result":"b"}`, "JSON object"},
		{"array", `["a"]`, "JSON object"},
		{"null record", `null`, "JSON object"},
		{"missing", `{"output":"hidden-value"}`, `missing text field "result"`},
		{"null field", `{"result":null}`, "must be a string"},
		{"number field", `{"result":123}`, "must be a string"},
		{"empty field", `{"result":""}`, "must not be empty"},
		{"invalid UTF-8", string([]byte{'{', '"', 'x', '"', ':', '"', 0xff, '"', '}'}), "UTF-8"},
		{"high surrogate", `{"result":"\ud800"}`, "unpaired Unicode surrogate"},
		{"low surrogate", `{"result":"\udc00"}`, "unpaired Unicode surrogate"},
		{"bad surrogate pair", `{"result":"\ud800\u0041"}`, "unpaired Unicode surrogate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := datasetFile(t, "{\"result\":\"ok\"}\n\n"+tc.record+"\n")
			_, err := source.LoadJSONL(path, "result", 4)
			if err == nil || !strings.Contains(err.Error(), path+":3:") || !strings.Contains(err.Error(), tc.message) || strings.Contains(err.Error(), "hidden-value") {
				t.Fatalf("error = %v", err)
			}
		})
	}
	for _, contents := range []string{"", "\n \r\n\t"} {
		path := datasetFile(t, contents)
		if _, err := source.LoadJSONL(path, "result", 4); err == nil || !strings.Contains(err.Error(), path+":1:") || !strings.Contains(err.Error(), "no records") {
			t.Fatalf("empty dataset: %v", err)
		}
	}
}

func TestReplayRejectsInvalidFileAndOptions(t *testing.T) {
	path := datasetFile(t, `{"result":"ok"}`)
	for _, tc := range []struct {
		path, field string
		width       int
	}{
		{path, "", 4}, {path, "result", 0}, {path, "result", source.MaxChunkChars + 1},
		{filepath.Join(t.TempDir(), "missing.jsonl"), "result", 4},
		{t.TempDir(), "result", 4},
	} {
		if _, err := source.LoadJSONL(tc.path, tc.field, tc.width); err == nil || !strings.Contains(err.Error(), tc.path+":1:") {
			t.Fatalf("options %+v: %v", tc, err)
		}
	}
}

func TestReplayConcurrentReservations(t *testing.T) {
	var data strings.Builder
	for i := range 10 {
		fmt.Fprintf(&data, "{\"result\":\"record-%d\"}\n", i)
	}
	replay, err := source.LoadJSONL(datasetFile(t, data.String()), "result", 3)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan string, 1000)
	var wg sync.WaitGroup
	for range 1000 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			chunks, err := replay.Next()
			if err != nil {
				t.Error(err)
				return
			}
			results <- strings.Join(chunks, "")
		}()
	}
	wg.Wait()
	close(results)
	counts := make(map[string]int)
	for result := range results {
		counts[result]++
	}
	for i := range 10 {
		if counts[fmt.Sprintf("record-%d", i)] != 100 {
			t.Fatalf("assignment counts: %v", counts)
		}
	}
}

func TestReplayRecordLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		length int
		width  int
		valid  bool
		marker string
	}{
		{"chunk boundary", source.MaxChunks, 1, true, ""},
		{"too many chunks", source.MaxChunks + 1, 1, false, "chunks"},
		{"decoded byte boundary", source.MaxOutputBytes, source.MaxChunkChars, true, ""},
		{"too many decoded bytes", source.MaxOutputBytes + 1, source.MaxChunkChars, false, "decoded text"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := datasetFile(t, "\n{\"result\":\""+strings.Repeat("x", tc.length)+"\"}")
			replay, err := source.LoadJSONL(path, "result", tc.width)
			if tc.valid {
				if err != nil || len(strings.Join(nextReplay(t, replay), "")) != tc.length {
					t.Fatalf("boundary rejected: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), path+":2:") || !strings.Contains(err.Error(), tc.marker) {
				t.Fatalf("limit error: %v", err)
			}
		})
	}
}

func TestReplayLineLengthBoundary(t *testing.T) {
	prefix, suffix := `{"result":"ok","padding":"`, `"}`
	line := prefix + strings.Repeat("x", source.MaxJSONLLineBytes-len(prefix)-len(suffix)) + suffix
	for _, ending := range []string{"", "\n", "\r\n"} {
		path := datasetFile(t, line+ending)
		if _, err := source.LoadJSONL(path, "result", 4); err != nil {
			t.Fatalf("exact line boundary with %q: %v", ending, err)
		}
	}
	path := datasetFile(t, "\n "+line+"\n")
	if _, err := source.LoadJSONL(path, "result", 4); err == nil || !strings.Contains(err.Error(), path+":2:") || !strings.Contains(err.Error(), "line exceeds") {
		t.Fatalf("oversized line: %v", err)
	}
}

func TestReplayDatasetRecordLimit(t *testing.T) {
	path := datasetFile(t, strings.Repeat("{\"result\":\"x\"}\n", source.MaxDatasetRecords))
	replay, err := source.LoadJSONL(path, "result", 4)
	if err != nil || replay.Len() != source.MaxDatasetRecords {
		t.Fatalf("record limit boundary: %v", err)
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString("\n{\"result\":\"extra\"}")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.LoadJSONL(path, "result", 4); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%s:%d:", path, source.MaxDatasetRecords+2)) || !strings.Contains(err.Error(), "records") {
		t.Fatalf("record count error: %v", err)
	}
}

func TestReplayDatasetByteLimitCountsBlankLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded.jsonl")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	record := "{\"result\":\"ok\"}\n"
	if _, err := file.WriteString(record); err != nil {
		t.Fatal(err)
	}
	remaining, lines := source.MaxDatasetBytes-len(record), 1
	for remaining > 0 {
		size := min(64*1024, remaining)
		if _, err := file.WriteString(strings.Repeat(" ", size-1) + "\n"); err != nil {
			t.Fatal(err)
		}
		remaining -= size
		lines++
	}
	_ = file.Close()
	if _, err := source.LoadJSONL(path, "result", 4); err != nil {
		t.Fatalf("exact file size boundary: %v", err)
	}
	file, err = os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(" ")
	_ = file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.LoadJSONL(path, "result", 4); err == nil || !strings.Contains(err.Error(), fmt.Sprintf("%s:%d:", path, lines+1)) || !strings.Contains(err.Error(), "dataset exceeds") {
		t.Fatalf("oversized dataset: %v", err)
	}
}
