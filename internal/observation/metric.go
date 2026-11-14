package observation

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const (
	MetricTypeEntityUsage           = "entity_usage"
	MetricTypeTranslationBatchUsage = "translation_batch_usage"
	MetricTypeTotalUsage            = "total_usage"
)

// TokenUsage tracks LLM token consumption reported by Responses API usage metadata.
type TokenUsage struct {
	InputTokens      int64 `json:"input_tokens"`
	CachedTokens     int64 `json:"cached_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	ReasoningTokens  int64 `json:"reasoning_tokens"`
	TotalTokens      int64 `json:"total_tokens"`
}

// Add accumulates other into u.
func (u *TokenUsage) Add(other TokenUsage) {
	u.InputTokens += other.InputTokens
	u.CachedTokens += other.CachedTokens
	u.CacheWriteTokens += other.CacheWriteTokens
	u.OutputTokens += other.OutputTokens
	u.ReasoningTokens += other.ReasoningTokens
	u.TotalTokens += other.TotalTokens
}

// CallMetric represents one JSONL metric event for a single LLM request attempt
// (type "entity_usage" or "translation_batch_usage").
type CallMetric struct {
	Type       string     `json:"type"`
	RunID      string     `json:"run_id"`
	Timestamp  time.Time  `json:"timestamp"`
	Model      string     `json:"model"`
	Batch      int        `json:"batch,omitzero"`
	TotalBatch int        `json:"total_batch,omitzero"`
	Attempt    int        `json:"attempt"`
	Status     string     `json:"status"`
	DurationMS int64      `json:"duration_ms"`
	Usage      TokenUsage `json:"usage"`
	Error      string     `json:"error,omitzero"`
}

// TotalMetric represents the final JSONL summary event (type "total_usage")
// written when an execution finishes.
type TotalMetric struct {
	Type       string     `json:"type"`
	RunID      string     `json:"run_id"`
	Timestamp  time.Time  `json:"timestamp"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt time.Time  `json:"finished_at"`
	DurationMS int64      `json:"duration_ms"`
	Requests   int        `json:"requests"`
	Errors     int        `json:"errors"`
	Retries    int        `json:"retries"`
	Usage      TokenUsage `json:"usage"`
}

// Metrics collects thread-safe per-attempt timing and token usage and optionally
// streams each JSONL metric event to an io.Writer as it occurs.
type Metrics struct {
	mu        sync.Mutex
	runID     string
	startedAt time.Time
	calls     []CallMetric
	requests  int
	errors    int
	retries   int
	usage     TokenUsage
	out       io.Writer
	writeErr  error
}

// NewMetrics creates a Metrics collector bound to runID that streams JSONL events
// to out when out is non-nil.
func NewMetrics(runID string, out io.Writer) *Metrics {
	return &Metrics{
		runID:     runID,
		startedAt: time.Now().UTC(),
		calls:     make([]CallMetric, 0),
		out:       out,
	}
}

// RunID returns the execution identifier attached to emitted metric events.
func (m *Metrics) RunID() string {
	if m == nil {
		return ""
	}
	return m.runID
}

// RecordLLMCall records one LLM API attempt and immediately writes a JSONL
// event if an output writer is configured. Calling on a nil receiver is a no-op.
func (m *Metrics) RecordLLMCall(call CallMetric) {
	if m == nil {
		return
	}
	if call.Timestamp.IsZero() {
		call.Timestamp = time.Now().UTC()
	} else {
		call.Timestamp = call.Timestamp.UTC()
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if call.RunID == "" {
		call.RunID = m.runID
	}

	m.requests++
	if call.Status != "ok" {
		m.errors++
	}
	if call.Attempt > 1 {
		m.retries++
	}
	m.usage.Add(call.Usage)
	m.calls = append(m.calls, call)

	if err := writeJSONLine(m.out, call); err != nil {
		m.writeErr = errors.Join(m.writeErr, err)
	}
}

// Snapshot returns a point-in-time TotalMetric summary without writing to output.
func (m *Metrics) Snapshot() TotalMetric {
	if m == nil {
		now := time.Now().UTC()
		return TotalMetric{
			Type:       MetricTypeTotalUsage,
			Timestamp:  now,
			StartedAt:  now,
			FinishedAt: now,
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	return m.totalLocked(time.Now().UTC())
}

// Calls returns a copy of all recorded CallMetric events so far.
func (m *Metrics) Calls() []CallMetric {
	if m == nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	out := make([]CallMetric, len(m.calls))
	copy(out, m.calls)
	return out
}

// WriteTotal emits the final "total_usage" JSONL event to the configured writer
// (if any) and returns any accumulated metric write errors.
func (m *Metrics) WriteTotal() error {
	if m == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	total := m.totalLocked(time.Now().UTC())
	if err := writeJSONLine(m.out, total); err != nil {
		m.writeErr = errors.Join(m.writeErr, err)
	}
	return m.writeErr
}

func (m *Metrics) totalLocked(now time.Time) TotalMetric {
	return TotalMetric{
		Type:       MetricTypeTotalUsage,
		RunID:      m.runID,
		Timestamp:  now,
		StartedAt:  m.startedAt,
		FinishedAt: now,
		DurationMS: now.Sub(m.startedAt).Milliseconds(),
		Requests:   m.requests,
		Errors:     m.errors,
		Retries:    m.retries,
		Usage:      m.usage,
	}
}

func writeJSONLine(w io.Writer, v any) error {
	if w == nil {
		return nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("observation: encode metric JSON: %w", err)
	}
	b = append(b, '\n')
	if _, err := w.Write(b); err != nil {
		return fmt.Errorf("observation: write metric JSON line: %w", err)
	}
	return nil
}
