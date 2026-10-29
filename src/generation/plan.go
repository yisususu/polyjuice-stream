// Package generation shares response content, limits and pacing across formats.
package generation

import (
	"errors"
	"time"
)

// Plan is a selected response and its server-side pacing parameters.
// Chunks are simulated output units, not model tokens. Treat a plan as immutable.
type Plan struct {
	Chunks       []string
	FirstDelay   time.Duration
	Interval     time.Duration
	FinishReason string
}

// NewPlan applies an optional chunk cap; zero means no cap. A limit that is
// equal to or larger than the selected output does not truncate the response.
func NewPlan(chunks []string, firstDelay, interval time.Duration, limit int) (Plan, error) {
	if len(chunks) == 0 {
		return Plan{}, errors.New("response must contain at least one chunk")
	}
	if firstDelay < 0 || interval < 0 {
		return Plan{}, errors.New("generation delays must not be negative")
	}
	if limit < 0 {
		return Plan{}, errors.New("chunk limit must not be negative")
	}
	count, reason := len(chunks), "stop"
	if limit > 0 && limit < count {
		count, reason = limit, "length"
	}
	return Plan{
		Chunks:       append([]string(nil), chunks[:count]...),
		FirstDelay:   firstDelay,
		Interval:     interval,
		FinishReason: reason,
	}, nil
}
