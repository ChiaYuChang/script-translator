package llm

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/openai/openai-go/v3"

	"go-script-translate/internal/observation"
)

const (
	// EndpointResponses selects the OpenAI Responses API (/v1/responses).
	EndpointResponses = "responses"
	// EndpointChatCompletions selects the Chat Completions API (/v1/chat/completions).
	EndpointChatCompletions = "chat_completions"
)

// ProviderOption configures optional instrumentation and pacing on Provider.
type ProviderOption func(*Provider)

// WithEndpoint selects which OpenAI-compatible endpoint protocol Provider calls
// ("responses" or "chat_completions"). Blank defaults to "responses".
func WithEndpoint(endpoint string) ProviderOption {
	return func(p *Provider) {
		p.endpoint = strings.ToLower(strings.TrimSpace(endpoint))
	}
}

// WithLogger sets the structured logger used by Provider.
func WithLogger(logger *slog.Logger) ProviderOption {
	return func(p *Provider) {
		p.logger = logger
	}
}

// WithMetrics attaches a metrics collector for recording request latency and token usage.
func WithMetrics(metrics *observation.Metrics) ProviderOption {
	return func(p *Provider) {
		p.metrics = metrics
	}
}

// WithWaitTime sets the interval waited before each subsequent LLM call/batch.
// A zero or negative duration disables inter-call waiting.
func WithWaitTime(d time.Duration) ProviderOption {
	return func(p *Provider) {
		if d > 0 {
			p.waitTime = d
		} else {
			p.waitTime = 0
		}
	}
}

// WithRetryTime sets the base backoff duration used on 429 / 5xx / EOF retries
// (retry k waits RetryTime * (1 << k)). Zero or negative disables backoff.
func WithRetryTime(d time.Duration) ProviderOption {
	return func(p *Provider) {
		if d > 0 {
			p.retryTime = d
		} else {
			p.retryTime = 0
		}
	}
}

func withSleep(fn func(context.Context, time.Duration) error) ProviderOption {
	return func(p *Provider) {
		p.sleepFn = fn
	}
}

// Provider wraps one OpenAI client. Models and per-call options are
// passed to each method so entity recognition and translation can use distinct models.
type Provider struct {
	client    *openai.Client
	endpoint  string
	logger    *slog.Logger
	metrics   *observation.Metrics
	waitTime  time.Duration
	retryTime time.Duration
	sleepFn   func(context.Context, time.Duration) error
	hasCalled bool
}

func NewProvider(client *openai.Client, opts ...ProviderOption) *Provider {
	p := &Provider{
		client:   client,
		endpoint: EndpointResponses,
		sleepFn:  sleepWithContext,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	if p.endpoint == "" {
		p.endpoint = EndpointResponses
	}
	return p
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (p *Provider) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	if p != nil && p.sleepFn != nil {
		return p.sleepFn(ctx, d)
	}
	return sleepWithContext(ctx, d)
}

func (p *Provider) paceCall(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if p.hasCalled && p.waitTime > 0 {
		if err := p.sleep(ctx, p.waitTime); err != nil {
			return err
		}
	}
	p.hasCalled = true
	return nil
}

func (p *Provider) retryDelay(err error, retryIdx int) (time.Duration, bool) {
	if p == nil {
		return 0, false
	}
	if isBackoffError(err) {
		if p.retryTime <= 0 {
			return 0, false
		}
		shift := min(max(retryIdx, 0), 30)
		delay := p.retryTime
		for i := 0; i < shift; i++ {
			if delay > math.MaxInt64/2 {
				return math.MaxInt64, true
			}
			delay *= 2
		}
		return delay, true
	}
	return p.waitTime, false
}

func isBackoffError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *openai.Error
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == http.StatusTooManyRequests ||
			apiErr.StatusCode >= http.StatusInternalServerError
	}
	if isUnexpectedEOFError(err) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func isUnexpectedEOFError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var synErr *jsontext.SyntacticError
	if errors.As(err, &synErr) {
		return false
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return errors.Is(urlErr, io.ErrUnexpectedEOF) ||
			errors.Is(urlErr, io.EOF) ||
			strings.Contains(strings.ToLower(urlErr.Error()), "unexpected eof")
	}
	return errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
}

func (p *Provider) log() *slog.Logger {
	if p != nil && p.logger != nil {
		return p.logger
	}
	return slog.Default()
}

func (p *Provider) recordAttempt(
	ctx context.Context,
	metricType string,
	modelName string,
	batch int,
	totalBatch int,
	batchSize int,
	attempt int,
	started time.Time,
	usage observation.TokenUsage,
	err error,
) {
	durationMS := time.Since(started).Milliseconds()
	call := observation.CallMetric{
		Type:       metricType,
		Model:      modelName,
		Batch:      batch,
		TotalBatch: totalBatch,
		Attempt:    attempt,
		Status:     "ok",
		DurationMS: durationMS,
		Usage:      usage,
	}
	if err != nil {
		call.Status = "error"
		call.Error = err.Error()
	}
	p.metrics.RecordLLMCall(call)

	if err == nil {
		attrs := []any{
			slog.String("type", metricType),
			slog.String("model", modelName),
			slog.Int("attempt", attempt),
			slog.Int64("duration_ms", durationMS),
			slog.Int64("input_tokens", usage.InputTokens),
			slog.Int64("cached_tokens", usage.CachedTokens),
			slog.Int64("output_tokens", usage.OutputTokens),
			slog.Int64("reasoning_tokens", usage.ReasoningTokens),
			slog.Int64("total_tokens", usage.TotalTokens),
		}
		if batch > 0 {
			attrs = append(attrs, slog.Int("batch", batch))
			if totalBatch > 0 {
				attrs = append(attrs, slog.Int("total_batch", totalBatch))
			}
			if batchSize > 0 {
				attrs = append(attrs, slog.Int("batch_size", batchSize))
			}
		}
		p.log().InfoContext(ctx, "llm request completed", attrs...)
	}
}
