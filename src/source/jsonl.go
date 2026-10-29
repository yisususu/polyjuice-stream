package source

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"unicode/utf8"
)

const (
	MaxDatasetBytes   = 64 * 1024 * 1024
	MaxJSONLLineBytes = 8 * 1024 * 1024
	MaxDatasetRecords = 100_000
)

type replayRecord struct {
	text   string
	chunks int
}

// Replay owns an immutable startup snapshot and a cyclic assignment cursor.
// Completion order and cancellation never roll back a reserved record.
type Replay struct {
	mu         sync.Mutex
	records    []replayRecord
	next       int
	chunkChars int
	maxChunks  int
}

// LoadJSONL validates every record before returning a source. Field is a literal
// top-level JSON key, not a dotted path. Blank physical lines are skipped.
func LoadJSONL(path, field string, chunkChars int) (*Replay, error) {
	fail := func(line int, message string) error {
		return fmt.Errorf("%s:%d: %s", path, line, message)
	}
	if field == "" {
		return nil, fail(1, "text field must not be empty")
	}
	if chunkChars < 1 || chunkChars > MaxChunkChars {
		return nil, fail(1, fmt.Sprintf("chunk-chars must be between 1 and %d", MaxChunkChars))
	}
	// Stat before opening so ordinary FIFO/device paths cannot block startup.
	info, err := os.Stat(path)
	if err != nil {
		return nil, fail(1, fmt.Sprintf("cannot access dataset: %v", err))
	}
	if !info.Mode().IsRegular() {
		return nil, fail(1, "dataset must be a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fail(1, fmt.Sprintf("cannot open dataset: %v", err))
	}
	defer file.Close()
	info, err = file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, fail(1, "dataset must be a readable regular file")
	}

	replay := &Replay{chunkChars: chunkChars}
	reader := bufio.NewReaderSize(io.LimitReader(file, MaxDatasetBytes+1), 64*1024)
	total, line := 0, 0
	for {
		line++
		data, err := readJSONLLine(reader, &total)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fail(line, err.Error())
		}
		if len(bytes.TrimSpace(data)) != 0 {
			if len(replay.records) == MaxDatasetRecords {
				return nil, fail(line, fmt.Sprintf("dataset exceeds %d records", MaxDatasetRecords))
			}
			record, err := decodeRecord(data, field, chunkChars)
			if err != nil {
				return nil, fail(line, err.Error())
			}
			replay.records = append(replay.records, record)
			replay.maxChunks = max(replay.maxChunks, record.chunks)
		}
		if errors.Is(err, io.EOF) {
			break
		}
	}
	if len(replay.records) == 0 {
		return nil, fail(1, "dataset contains no records")
	}
	return replay, nil
}

// readJSONLLine bounds allocation before appending each reader fragment. File
// limits count all bytes; line limits exclude the LF or CRLF line ending.
func readJSONLLine(reader *bufio.Reader, total *int) ([]byte, error) {
	var line []byte
	for {
		fragment, err := reader.ReadSlice('\n')
		*total += len(fragment)
		if *total > MaxDatasetBytes {
			return nil, fmt.Errorf("dataset exceeds %d bytes", MaxDatasetBytes)
		}
		if len(fragment) > MaxJSONLLineBytes+2-len(line) {
			return nil, fmt.Errorf("line exceeds %d bytes", MaxJSONLLineBytes)
		}
		line = append(line, fragment...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if len(line) > 0 && line[len(line)-1] == '\n' {
			line = line[:len(line)-1]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
		}
		if len(line) > MaxJSONLLineBytes {
			return nil, fmt.Errorf("line exceeds %d bytes", MaxJSONLLineBytes)
		}
		return line, err
	}
}

func decodeRecord(data []byte, field string, chunkChars int) (replayRecord, error) {
	if !utf8.Valid(data) {
		return replayRecord{}, errors.New("record is not valid UTF-8")
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return replayRecord{}, errors.New("record must be a single valid JSON object")
	}
	raw, ok := fields[field]
	if !ok {
		return replayRecord{}, fmt.Errorf("missing text field %q", field)
	}
	var text string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &text) != nil {
		return replayRecord{}, fmt.Errorf("text field %q must be a string", field)
	}
	if !validSurrogateEscapes(raw) {
		return replayRecord{}, fmt.Errorf("text field %q contains an unpaired Unicode surrogate", field)
	}
	if text == "" {
		return replayRecord{}, fmt.Errorf("text field %q must not be empty", field)
	}
	if len(text) > MaxOutputBytes {
		return replayRecord{}, fmt.Errorf("decoded text exceeds %d bytes", MaxOutputBytes)
	}
	chunks := (utf8.RuneCountInString(text) + chunkChars - 1) / chunkChars
	if chunks > MaxChunks {
		return replayRecord{}, fmt.Errorf("record exceeds %d chunks; increase chunk-chars", MaxChunks)
	}
	return replayRecord{text: text, chunks: chunks}, nil
}

// encoding/json replaces unpaired UTF-16 surrogate escapes with U+FFFD. Reject
// them explicitly so loading a record cannot silently change selected text.
// The caller has already checked JSON syntax and that raw is a string.
func validSurrogateEscapes(raw []byte) bool {
	for i := 1; i < len(raw)-1; i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		value, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		if value >= 0xdc00 && value <= 0xdfff {
			return false
		}
		if value >= 0xd800 && value <= 0xdbff {
			if i+7 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func (r *Replay) Len() int { return len(r.records) }

// MaxChunks reports the largest validated record, for startup timing checks.
func (r *Replay) MaxChunks() int { return r.maxChunks }

// Next reserves a record before doing any chunk work. Returned slices belong to
// the caller; their immutable strings reference the in-memory dataset snapshot.
func (r *Replay) Next() ([]string, error) {
	if r == nil || len(r.records) == 0 {
		return nil, errors.New("replay source has no records")
	}
	r.mu.Lock()
	record := r.records[r.next]
	r.next = (r.next + 1) % len(r.records)
	r.mu.Unlock()
	chunks := make([]string, 0, record.chunks)
	start, count := 0, 0
	for position := range record.text {
		if count == r.chunkChars {
			chunks = append(chunks, record.text[start:position])
			start, count = position, 0
		}
		count++
	}
	chunks = append(chunks, record.text[start:])
	return chunks, nil
}
