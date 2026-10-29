// Package source supplies response content without depending on HTTP delivery.
package source

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	mathrand "math/rand/v2"
	"sync"
)

const (
	MaxChunks      = 100_000
	MaxChunkChars  = 4096
	MaxOutputBytes = 4 * 1024 * 1024
)

// Random assigns each request an independent, deterministic random stream.
// A seed reproduces assignment order, not the order concurrent clients arrive.
type Random struct {
	mu       sync.Mutex
	seed     uint64
	sequence uint64
}

// NewRandom uses a fresh startup seed when seed is nil.
func NewRandom(seed *int64) (*Random, error) {
	var value uint64
	if seed == nil {
		var bytes [8]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return nil, fmt.Errorf("create random source seed: %w", err)
		}
		value = binary.LittleEndian.Uint64(bytes[:])
	} else {
		value = uint64(*seed)
	}
	return &Random{seed: value}, nil
}

// ValidateSize bounds both allocation size and the number of output events.
func ValidateSize(chunks, chunkChars int) error {
	if chunks < 1 || chunks > MaxChunks {
		return fmt.Errorf("chunks must be between 1 and %d", MaxChunks)
	}
	if chunkChars < 1 || chunkChars > MaxChunkChars {
		return fmt.Errorf("chunk-chars must be between 1 and %d", MaxChunkChars)
	}
	if chunks > MaxOutputBytes/chunkChars {
		return fmt.Errorf("output exceeds %d bytes; reduce chunks or chunk-chars", MaxOutputBytes)
	}
	return nil
}

// Next returns printable ASCII chunks. Generation runs outside the assignment
// lock, so a long response does not serialize the work of other requests.
func (r *Random) Next(chunks, chunkChars int) ([]string, error) {
	if err := ValidateSize(chunks, chunkChars); err != nil {
		return nil, err
	}
	r.mu.Lock()
	sequence := r.sequence
	r.sequence++
	r.mu.Unlock()

	rng := mathrand.New(mathrand.NewPCG(mix(r.seed), mix(sequence^r.seed^0x9e3779b97f4a7c15)))
	buffer := make([]byte, chunks*chunkChars)
	for i := range buffer {
		buffer[i] = byte(' ' + rng.IntN(95))
	}
	text := string(buffer)
	result := make([]string, chunks)
	for i := range result {
		result[i] = text[i*chunkChars : (i+1)*chunkChars]
	}
	return result, nil
}

// mix diffuses seed and sequence bits before initializing a per-request PCG.
func mix(value uint64) uint64 {
	value = (value ^ (value >> 30)) * 0xbf58476d1ce4e5b9
	value = (value ^ (value >> 27)) * 0x94d049bb133111eb
	return value ^ (value >> 31)
}
