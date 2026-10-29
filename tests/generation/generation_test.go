package generation_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/yisususu/polyjuice-stream/src/generation"
)

func plan(t *testing.T) generation.Plan {
	t.Helper()
	p, err := generation.NewPlan([]string{"Hel", "lo ", "LLM"}, 100*time.Millisecond, 30*time.Millisecond, 0)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestChunkLimitsAndFinishReason(t *testing.T) {
	for _, test := range []struct {
		limit  int
		text   string
		reason string
	}{
		{0, "Hello LLM", "stop"},
		{2, "Hello ", "length"},
		{3, "Hello LLM", "stop"},
		{5, "Hello LLM", "stop"},
	} {
		p, err := generation.NewPlan([]string{"Hel", "lo ", "LLM"}, 0, 0, test.limit)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Join(p.Chunks, "") != test.text || p.FinishReason != test.reason {
			t.Fatalf("limit %d: got %#v", test.limit, p)
		}
	}
}

func TestPlanRejectsInvalidValuesAndCopiesSelection(t *testing.T) {
	for _, test := range []struct {
		chunks   []string
		first    time.Duration
		interval time.Duration
		limit    int
	}{
		{nil, 0, 0, 0},
		{[]string{"x"}, -1, 0, 0},
		{[]string{"x"}, 0, -1, 0},
		{[]string{"x"}, 0, 0, -1},
	} {
		if _, err := generation.NewPlan(test.chunks, test.first, test.interval, test.limit); err == nil {
			t.Fatalf("accepted invalid plan: %#v", test)
		}
	}
	selected := []string{"你好", "🙂"}
	p, err := generation.NewPlan(selected, 0, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	selected[0] = "changed"
	if strings.Join(p.Chunks, "") != "你好🙂" {
		t.Fatal("changing a source slice changed the response plan")
	}
}

func TestPacingExtendsAfterBlockedEmissionWithoutCatchUp(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		var now time.Duration
		var starts []time.Duration
		var content strings.Builder
		wait := func(ctx context.Context, duration time.Duration) error {
			now += duration
			return ctx.Err()
		}
		err := generation.RunWithWait(context.Background(), plan(t), func(chunk string) error {
			starts = append(starts, now)
			content.WriteString(chunk)
			if blocked && len(starts) == 2 {
				now += 100 * time.Millisecond
			}
			return nil
		}, wait)
		if err != nil {
			t.Fatal(err)
		}
		want := []time.Duration{100 * time.Millisecond, 130 * time.Millisecond, 160 * time.Millisecond}
		if blocked {
			want[2] = 260 * time.Millisecond
		}
		if !reflect.DeepEqual(starts, want) || content.String() != "Hello LLM" {
			t.Fatalf("blocked=%v: starts=%v, text=%q", blocked, starts, content.String())
		}
	}
}

func TestStreamingAndCollectingShareGenerationWaits(t *testing.T) {
	p := plan(t)
	var streamed []string
	var complete strings.Builder
	var schedules [2][]time.Duration
	for mode := range 2 {
		err := generation.RunWithWait(context.Background(), p, func(chunk string) error {
			if mode == 0 {
				streamed = append(streamed, chunk)
			} else {
				complete.WriteString(chunk)
			}
			return nil
		}, func(_ context.Context, duration time.Duration) error {
			schedules[mode] = append(schedules[mode], duration)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if complete.String() != strings.Join(streamed, "") || !reflect.DeepEqual(schedules[0], schedules[1]) {
		t.Fatal("response delivery format changed the content or generation waits")
	}
	if len(schedules[0]) != len(p.Chunks) {
		t.Fatal("generation must not wait after the final chunk")
	}
}

func TestEmitterFailureStopsFurtherWaitsAndWrites(t *testing.T) {
	failure := errors.New("connection closed")
	waits, writes := 0, 0
	err := generation.RunWithWait(context.Background(), plan(t), func(string) error {
		writes++
		return failure
	}, func(context.Context, time.Duration) error {
		waits++
		return nil
	})
	if !errors.Is(err, failure) || waits != 1 || writes != 1 {
		t.Fatalf("err=%v, waits=%d, writes=%d", err, waits, writes)
	}
}

func TestCancellationBeforeAndAfterWaitStopsEmission(t *testing.T) {
	for _, cancelBefore := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		if cancelBefore {
			cancel()
		}
		writes := 0
		err := generation.RunWithWait(ctx, plan(t), func(string) error {
			writes++
			return nil
		}, func(context.Context, time.Duration) error {
			cancel()
			return nil
		})
		cancel()
		if !errors.Is(err, context.Canceled) || writes != 0 {
			t.Fatalf("cancelBefore=%v: err=%v, writes=%d", cancelBefore, err, writes)
		}
	}
}

func TestCancellationAfterEmissionStopsNextWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waits, writes := 0, 0
	err := generation.RunWithWait(ctx, plan(t), func(string) error {
		writes++
		cancel()
		return nil
	}, func(context.Context, time.Duration) error {
		waits++
		return nil
	})
	if !errors.Is(err, context.Canceled) || waits != 1 || writes != 1 {
		t.Fatalf("err=%v, waits=%d, writes=%d", err, waits, writes)
	}
}

func TestProductionWaitCanBeCancelled(t *testing.T) {
	p := plan(t)
	p.FirstDelay = time.Hour
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- generation.Run(ctx, p, func(string) error {
			return errors.New("unexpected emission")
		})
	}()
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not interrupt generation")
	}
}
