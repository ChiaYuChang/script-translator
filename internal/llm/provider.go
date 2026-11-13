package llm

import (
	"context"
	"log/slog"
	"time"

	"github.com/openai/openai-go/v3"

	"go-script-translate/internal/observation"
)

// ProviderOption configures optional instrumentation on Provider.
type ProviderOption func(*Provider)

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

// Provider wraps one OpenAI-compatible client. Models and per-call options are
// passed to each method so entity recognition and translation can use distinct models.
type Provider struct {
	client  *openai.Client
	logger  *slog.Logger
	metrics *observation.Metrics
}

func NewProvider(client *openai.Client, opts ...ProviderOption) *Provider {
	p := &Provider{client: client}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}
	return p
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
		}
		p.log().InfoContext(ctx, "llm request completed", attrs...)
	}
}
