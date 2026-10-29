package source_test

import (
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yisususu/polyjuice-stream/src/source"
)

func seeded(t *testing.T, seed int64) *source.Random {
	t.Helper()
	r, err := source.NewRandom(&seed)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func next(t *testing.T, r *source.Random, chunks, chars int) []string {
	t.Helper()
	result, err := r.Next(chunks, chars)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestFixedSeedReproducesRequestSequenceAndPrefixes(t *testing.T) {
	a, b := seeded(t, -42), seeded(t, -42)
	full := next(t, a, 8, 17)
	prefix := next(t, b, 3, 17)
	if !reflect.DeepEqual(full[:3], prefix) {
		t.Fatal("a smaller response must preserve the same request's prefix")
	}
	for request := 0; request < 5; request++ {
		if !reflect.DeepEqual(next(t, a, 6, 13), next(t, b, 6, 13)) {
			t.Fatal("response length must not change later request sequences")
		}
	}
}

func TestPrintableASCIIAndRequestedSize(t *testing.T) {
	r, err := source.NewRandom(nil)
	if err != nil {
		t.Fatal(err)
	}
	chunks := next(t, r, 50, 41)
	if len(chunks) != 50 {
		t.Fatalf("got %d chunks", len(chunks))
	}
	for _, chunk := range chunks {
		if len(chunk) != 41 {
			t.Fatalf("got %d bytes in a chunk", len(chunk))
		}
		for _, character := range chunk {
			if character < ' ' || character > '~' {
				t.Fatalf("non-printable character: %q", character)
			}
		}
	}
}

func TestInvalidSizeDoesNotConsumeARequest(t *testing.T) {
	a, b := seeded(t, 1), seeded(t, 1)
	if _, err := a.Next(0, 4); err == nil {
		t.Fatal("expected a size error")
	}
	if !reflect.DeepEqual(next(t, a, 10, 4), next(t, b, 10, 4)) {
		t.Fatal("invalid input consumed a request assignment")
	}
}

func TestSizeBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		chunks int
		chars  int
		valid  bool
	}{
		{"zero chunks", 0, 4, false},
		{"negative chunks", -1, 4, false},
		{"too many chunks", source.MaxChunks + 1, 1, false},
		{"zero width", 1, 0, false},
		{"negative width", 1, -1, false},
		{"excess width", 1, source.MaxChunkChars + 1, false},
		{"excess bytes", 1025, 4096, false},
		{"maximum bytes", 1024, 4096, true},
		{"maximum chunks", source.MaxChunks, 1, true},
		{"default-sized response", 100, 4, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := source.ValidateSize(test.chunks, test.chars); (err == nil) != test.valid {
				t.Fatalf("ValidateSize(%d, %d) = %v", test.chunks, test.chars, err)
			}
		})
	}
}

func TestConcurrentAssignmentMatchesSequentialSequence(t *testing.T) {
	const requests = 64
	sequential, concurrent := seeded(t, 1729), seeded(t, 1729)
	want := make(map[string]int)
	for range requests {
		want[strings.Join(next(t, sequential, 7, 23), "")]++
	}
	results := make(chan string, requests)
	errors := make(chan error, requests)
	var group sync.WaitGroup
	for range requests {
		group.Add(1)
		go func() {
			defer group.Done()
			chunks, err := concurrent.Next(7, 23)
			if err != nil {
				errors <- err
				return
			}
			results <- strings.Join(chunks, "")
		}()
	}
	group.Wait()
	close(results)
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	got := make(map[string]int)
	for text := range results {
		got[text]++
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("concurrent requests lost or duplicated sequence assignments")
	}
}
