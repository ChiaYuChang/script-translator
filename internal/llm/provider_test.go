package llm

import (
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"

	"go-script-translate/internal/model"
	"go-script-translate/internal/observation"
)

func TestProviderRetryDelayAndStatusClassification(t *testing.T) {
	p := NewProvider(nil, WithWaitTime(2*time.Second), WithRetryTime(5*time.Second))

	err429 := &openai.Error{StatusCode: http.StatusTooManyRequests}
	err503 := &openai.Error{StatusCode: http.StatusServiceUnavailable}
	err500 := &openai.Error{StatusCode: http.StatusInternalServerError}
	err400 := &openai.Error{StatusCode: http.StatusBadRequest}
	errURLUnexpectedEOF := &url.Error{Op: "Post", URL: "https://api.example.invalid/v1/responses", Err: io.ErrUnexpectedEOF}
	errRawEOF := io.EOF
	errGeneric := errors.New("parse JSON: unexpected EOF")

	for _, tc := range []struct {
		name        string
		err         error
		retryIdx    int
		wantDelay   time.Duration
		wantBackoff bool
	}{
		{name: "429_1st_retry", err: err429, retryIdx: 0, wantDelay: 5 * time.Second, wantBackoff: true},
		{name: "429_2nd_retry", err: err429, retryIdx: 1, wantDelay: 10 * time.Second, wantBackoff: true},
		{name: "429_3rd_retry", err: err429, retryIdx: 2, wantDelay: 20 * time.Second, wantBackoff: true},
		{name: "429_4th_retry", err: err429, retryIdx: 3, wantDelay: 40 * time.Second, wantBackoff: true},
		{name: "503_1st_retry", err: err503, retryIdx: 0, wantDelay: 5 * time.Second, wantBackoff: true},
		{name: "500_2nd_retry", err: err500, retryIdx: 1, wantDelay: 10 * time.Second, wantBackoff: true},
		{name: "url_unexpected_eof_1st_retry", err: errURLUnexpectedEOF, retryIdx: 0, wantDelay: 5 * time.Second, wantBackoff: true},
		{name: "url_unexpected_eof_2nd_retry", err: errURLUnexpectedEOF, retryIdx: 1, wantDelay: 10 * time.Second, wantBackoff: true},
		{name: "raw_eof_1st_retry", err: errRawEOF, retryIdx: 0, wantDelay: 5 * time.Second, wantBackoff: true},
		{name: "400_uses_wait_time", err: err400, retryIdx: 2, wantDelay: 2 * time.Second, wantBackoff: false},
		{name: "generic_uses_wait_time", err: errGeneric, retryIdx: 1, wantDelay: 2 * time.Second, wantBackoff: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotDelay, gotBackoff := p.retryDelay(tc.err, tc.retryIdx)
			if gotDelay != tc.wantDelay || gotBackoff != tc.wantBackoff {
				t.Fatalf("retryDelay(%v, %d) = (%v, %v), want (%v, %v)",
					tc.err, tc.retryIdx, gotDelay, gotBackoff, tc.wantDelay, tc.wantBackoff)
			}
		})
	}

	// RetryTime == 0 disables backoff on 429 even when WaitTime > 0.
	pNoBackoff := NewProvider(nil, WithWaitTime(3*time.Second), WithRetryTime(0))
	if delay, backoff := pNoBackoff.retryDelay(err429, 0); delay != 0 || backoff {
		t.Fatalf("expected (0, false) on 429 with RetryTime=0, got (%v, %v)", delay, backoff)
	}
	if delay, backoff := pNoBackoff.retryDelay(errGeneric, 0); delay != 3*time.Second || backoff {
		t.Fatalf("expected WaitTime (3s, false) on non-429 with RetryTime=0, got (%v, %v)", delay, backoff)
	}

	// Negative options clamp to zero.
	pNeg := NewProvider(nil, WithWaitTime(-2*time.Second), WithRetryTime(-5*time.Second))
	if pNeg.waitTime != 0 || pNeg.retryTime != 0 {
		t.Fatalf("expected negative durations clamped to 0, got wait=%v retry=%v", pNeg.waitTime, pNeg.retryTime)
	}

	// Huge retryTime saturates safely at math.MaxInt64 without overflowing to negative durations.
	pHuge := NewProvider(nil, WithRetryTime(time.Duration(math.MaxInt64/2+1)))
	if delay, backoff := pHuge.retryDelay(err429, 2); delay != math.MaxInt64 || !backoff {
		t.Fatalf("expected saturated MaxInt64 backoff, got (%v, %v)", delay, backoff)
	}
}

func TestTranslate429ExhaustedPreservesCompletedBatchesAndSkipsFinalSleep(t *testing.T) {
	var calls atomic.Int32
	var slept []time.Duration
	var checkpoints [][]model.TranslatedText

	metrics := observation.NewMetrics("run-429-exhausted", nil)
	p := newTestProvider(
		t,
		func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				// Batch 1 succeeds.
				writeBatchResponse(t, w, []model.TranslatedText{{Index: 0, Text: "「早安」"}})
				return
			}
			// Batch 2 fails all 4 attempts with HTTP 429.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit exceeded","type":"rate_limit_error"}}`))
		},
		WithMetrics(metrics),
		WithWaitTime(1*time.Second),
		WithRetryTime(2*time.Second),
		withSleep(func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}),
	)

	_, err := p.Translate(context.Background(), model.TranslateRequest{
		Dialogue: []model.TranslateUnit{
			{Index: 0, Speaker: "A", Text: "「おはよう」"},
			{Index: 1, Speaker: "A", Text: "「おやすみ」"},
		},
	}, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  1,
		MaxAttempt: 4,
		OnBatchComplete: func(completed []model.TranslatedText) error {
			checkpoints = append(checkpoints, slices.Clone(completed))
			return nil
		},
	})
	if err == nil {
		t.Fatal("expected Translate to fail after exhausting 4 attempts on HTTP 429")
	}
	if !strings.Contains(err.Error(), "batch 2/2 failed after 4 attempts") || !strings.Contains(err.Error(), "429") {
		t.Fatalf("unexpected error message: %v", err)
	}

	// WaitTime once before batch 2, then 3 exponential backoff waits (2s, 4s, 8s);
	// 4th (final) failure must NOT sleep.
	wantSlept := []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
	}
	if !reflect.DeepEqual(slept, wantSlept) {
		t.Fatalf("recorded sleeps = %v, want %v", slept, wantSlept)
	}

	if len(checkpoints) != 1 || len(checkpoints[0]) != 1 || checkpoints[0][0].Index != 0 {
		t.Fatalf("expected batch 1 checkpoint preserved before batch 2 failure, got %#v", checkpoints)
	}

	report := metrics.Snapshot()
	if report.Requests != 5 || report.Errors != 4 || report.Retries != 3 {
		t.Fatalf("unexpected metrics snapshot: %#v", report)
	}
}

func TestTranslate429BackoffAbortedOnContextCancel(t *testing.T) {
	var calls atomic.Int32
	got429 := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	p := newTestProvider(
		t,
		func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				defer close(got429)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"rate limit","type":"rate_limit_error"}}`))
		},
		WithRetryTime(10*time.Second),
	)

	go func() {
		<-got429
		// Give Translate a brief moment to enter sleepWithContext, then cancel.
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := p.Translate(ctx, model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 0, Text: "「おはよう」"}},
	}, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  1,
		MaxAttempt: 3,
	})
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled on aborted 429 backoff sleep, got: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected only 1 HTTP call before backoff cancellation, got %d", calls.Load())
	}
	if elapsed >= 2*time.Second {
		t.Fatalf("expected backoff wait to abort promptly on cancel, took %v", elapsed)
	}
}
