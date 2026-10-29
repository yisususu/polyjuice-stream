package generation

import (
	"context"
	"errors"
	"time"
)

// Run waits before the first chunk and after each successful emission before
// sending the next. The emitter includes transport write and flush work; a slow
// emitter extends the request instead of causing a catch-up burst.
func Run(ctx context.Context, plan Plan, emit func(string) error) error {
	return RunWithWait(ctx, plan, emit, waitFor)
}

// RunWithWait allows deterministic pacing tests without sleeping. The supplied
// wait function must observe context cancellation, as the production waiter does.
// An emitter that blocks must enforce its own transport timeout or cancellation.
func RunWithWait(ctx context.Context, plan Plan, emit func(string) error, wait func(context.Context, time.Duration) error) error {
	if ctx == nil || emit == nil || wait == nil {
		return errors.New("generation requires a context, emitter and waiter")
	}
	if len(plan.Chunks) == 0 || plan.FirstDelay < 0 || plan.Interval < 0 {
		return errors.New("invalid generation plan")
	}
	for i, chunk := range plan.Chunks {
		if err := ctx.Err(); err != nil {
			return err
		}
		delay := plan.Interval
		if i == 0 {
			delay = plan.FirstDelay
		}
		if err := wait(ctx, delay); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := emit(chunk); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func waitFor(ctx context.Context, duration time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if duration == 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ctx.Err()
	}
}
