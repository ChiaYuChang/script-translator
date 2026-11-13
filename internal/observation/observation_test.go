package observation

import (
	"bytes"
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestInitConsoleFileLoggerAndStreamingJSONLMetrics(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "logs", "run.jsonl")
	metricPath := filepath.Join(dir, "metrics", "run.jsonl")

	var console bytes.Buffer
	obs, err := initWithWriter(Config{
		Log: LogConfig{
			Level:   "info",
			Console: ConsoleLogConfig{Enable: true},
			File:    FileLogConfig{Enable: true, File: logPath},
		},
		Metric: MetricConfig{
			Enable: true,
			File:   metricPath,
		},
	}, &console)
	if err != nil {
		t.Fatalf("initWithWriter() error: %v", err)
	}

	parsedRunID, err := uuid.Parse(obs.RunID)
	if err != nil {
		t.Fatalf("parse RunID %q: %v", obs.RunID, err)
	}
	if parsedRunID.Version() != 7 {
		t.Fatalf("expected UUIDv7 RunID, got version %d (%s)", parsedRunID.Version(), obs.RunID)
	}

	obs.Logger.Debug("hidden debug message")
	obs.Logger.Info("batch finished", "batch", 1, "input_tokens", 120)
	obs.Logger.Warn("retrying batch", "attempt", 2)

	obs.Metrics.RecordLLMCall(CallMetric{
		Type:       MetricTypeEntityUsage,
		Model:      "gpt-entity",
		Attempt:    1,
		Status:     "ok",
		DurationMS: 30,
		Usage: TokenUsage{
			InputTokens:  50,
			OutputTokens: 10,
			TotalTokens:  60,
		},
	})
	obs.Metrics.RecordLLMCall(CallMetric{
		Type:       MetricTypeTranslationBatchUsage,
		Model:      "gpt-test",
		Batch:      1,
		Attempt:    1,
		Status:     "error",
		DurationMS: 50,
		Error:      "validate schema: missing index",
		Usage: TokenUsage{
			InputTokens:  100,
			OutputTokens: 20,
			TotalTokens:  120,
		},
	})

	// Verify per-call metric events are already on disk before Close().
	midBytes, err := os.ReadFile(metricPath)
	if err != nil {
		t.Fatalf("read metric file before Close: %v", err)
	}
	midLines := strings.Split(strings.TrimSpace(string(midBytes)), "\n")
	if len(midLines) != 2 {
		t.Fatalf("expected 2 streamed metric lines before Close, got %d:\n%s", len(midLines), string(midBytes))
	}

	obs.Metrics.RecordLLMCall(CallMetric{
		Type:       MetricTypeTranslationBatchUsage,
		Model:      "gpt-test",
		Batch:      1,
		Attempt:    2,
		Status:     "ok",
		DurationMS: 80,
		Usage: TokenUsage{
			InputTokens:     100,
			CachedTokens:    80,
			OutputTokens:    40,
			ReasoningTokens: 10,
			TotalTokens:     140,
		},
	})

	if err := obs.Close(); err != nil {
		t.Fatalf("obs.Close() error: %v", err)
	}
	// Closing twice must remain idempotent and not write a second total_usage line.
	if err := obs.Close(); err != nil {
		t.Fatalf("second obs.Close() error: %v", err)
	}

	consoleOut := console.String()
	if strings.Contains(consoleOut, "hidden debug message") {
		t.Fatalf("expected debug message filtered out of console:\n%s", consoleOut)
	}
	if !strings.Contains(consoleOut, "batch finished") || !strings.Contains(consoleOut, "retrying batch") {
		t.Fatalf("expected info and warn logs in console:\n%s", consoleOut)
	}
	if !strings.Contains(consoleOut, "time=") || !strings.Contains(consoleOut, "run_id="+obs.RunID) {
		t.Fatalf("expected timestamp and run_id in console output:\n%s", consoleOut)
	}

	logBytes, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	logLines := strings.Split(strings.TrimSpace(string(logBytes)), "\n")
	if len(logLines) != 2 {
		t.Fatalf("expected 2 JSON log lines, got %d:\n%s", len(logLines), string(logBytes))
	}
	for _, line := range logLines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON log line %q: %v", line, err)
		}
		if entry["run_id"] != obs.RunID {
			t.Fatalf("expected log run_id %q, got %v", obs.RunID, entry["run_id"])
		}
		rawTime, ok := entry["time"].(string)
		if !ok || rawTime == "" {
			t.Fatalf("expected non-empty string time in log entry: %#v", entry)
		}
		if _, err := time.Parse(time.RFC3339Nano, rawTime); err != nil {
			t.Fatalf("invalid RFC3339Nano log timestamp %q: %v", rawTime, err)
		}
	}

	metricBytes, err := os.ReadFile(metricPath)
	if err != nil {
		t.Fatalf("read metric file: %v", err)
	}
	metricLines := strings.Split(strings.TrimSpace(string(metricBytes)), "\n")
	if len(metricLines) != 4 {
		t.Fatalf("expected 4 JSONL metric lines (3 calls + 1 total_usage), got %d:\n%s", len(metricLines), string(metricBytes))
	}

	var calls []CallMetric
	for i := range 3 {
		var c CallMetric
		if err := json.Unmarshal([]byte(metricLines[i]), &c); err != nil {
			t.Fatalf("unmarshal metric call line %d: %v", i, err)
		}
		if c.RunID != obs.RunID {
			t.Fatalf("call[%d] run_id = %q, want %q", i, c.RunID, obs.RunID)
		}
		if c.Timestamp.IsZero() {
			t.Fatalf("call[%d] missing timestamp", i)
		}
		calls = append(calls, c)
	}
	if calls[0].Type != MetricTypeEntityUsage ||
		calls[1].Type != MetricTypeTranslationBatchUsage ||
		calls[2].Type != MetricTypeTranslationBatchUsage {
		t.Fatalf("unexpected call types: %#v", calls)
	}

	var total TotalMetric
	if err := json.Unmarshal([]byte(metricLines[3]), &total); err != nil {
		t.Fatalf("unmarshal total_usage line: %v", err)
	}
	if total.Type != MetricTypeTotalUsage || total.RunID != obs.RunID || total.Timestamp.IsZero() {
		t.Fatalf("unexpected total_usage header: %#v", total)
	}
	if total.Requests != 3 || total.Errors != 1 || total.Retries != 1 {
		t.Fatalf("unexpected total counts: %#v", total)
	}
	if total.Usage.InputTokens != 250 ||
		total.Usage.CachedTokens != 80 ||
		total.Usage.OutputTokens != 70 ||
		total.Usage.ReasoningTokens != 10 ||
		total.Usage.TotalTokens != 320 {
		t.Fatalf("unexpected aggregated token usage: %#v", total.Usage)
	}
}

func TestMultipleRunsAppendWithDistinctRunIDs(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "shared.jsonl")
	metricPath := filepath.Join(dir, "metrics.jsonl")
	cfg := Config{
		Log: LogConfig{
			Level:   "info",
			Console: ConsoleLogConfig{Enable: false},
			File:    FileLogConfig{Enable: true, File: logPath},
		},
		Metric: MetricConfig{
			Enable: true,
			File:   metricPath,
		},
	}

	obs1, err := initWithWriter(cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("first init failed: %v", err)
	}
	obs1.Logger.Info("run one")
	obs1.Metrics.RecordLLMCall(CallMetric{Type: MetricTypeEntityUsage, Model: "m", Attempt: 1, Status: "ok"})
	if err := obs1.Close(); err != nil {
		t.Fatalf("first Close failed: %v", err)
	}

	obs2, err := initWithWriter(cfg, &bytes.Buffer{})
	if err != nil {
		t.Fatalf("second init failed: %v", err)
	}
	obs2.Logger.Info("run two")
	obs2.Metrics.RecordLLMCall(CallMetric{Type: MetricTypeEntityUsage, Model: "m", Attempt: 1, Status: "ok"})
	if err := obs2.Close(); err != nil {
		t.Fatalf("second Close failed: %v", err)
	}

	if obs1.RunID == obs2.RunID {
		t.Fatalf("expected distinct UUIDv7 RunIDs across runs, both were %q", obs1.RunID)
	}

	metricBytes, err := os.ReadFile(metricPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(metricBytes)), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 total lines across 2 appended runs, got %d", len(lines))
	}
}

func TestInitDisabledTargetsCreatesNoFiles(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "should-not-exist.jsonl")
	metricPath := filepath.Join(dir, "should-not-exist-metrics.jsonl")

	var console bytes.Buffer
	obs, err := initWithWriter(Config{
		Log: LogConfig{
			Level:   "info",
			Console: ConsoleLogConfig{Enable: false},
			File:    FileLogConfig{Enable: false, File: logPath},
		},
		Metric: MetricConfig{
			Enable: false,
			File:   metricPath,
		},
	}, &console)
	if err != nil {
		t.Fatalf("initWithWriter() error: %v", err)
	}
	obs.Logger.Info("discarded")
	obs.Metrics.RecordLLMCall(CallMetric{Type: MetricTypeEntityUsage, Model: "m", Attempt: 1, Status: "ok"})
	if err := obs.Close(); err != nil {
		t.Fatalf("Close() error: %v", err)
	}

	if console.Len() != 0 {
		t.Fatalf("expected empty console output when disabled, got %q", console.String())
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("expected no log file, stat err=%v", err)
	}
	if _, err := os.Stat(metricPath); !os.IsNotExist(err) {
		t.Fatalf("expected no metric file, stat err=%v", err)
	}
}

func TestConfigValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{
			name: "invalid log level",
			cfg:  Config{Log: LogConfig{Level: "trace"}},
		},
		{
			name: "file log enabled with empty path",
			cfg:  Config{Log: LogConfig{File: FileLogConfig{Enable: true}}},
		},
		{
			name: "metric enabled with empty path",
			cfg:  Config{Metric: MetricConfig{Enable: true}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := initWithWriter(tc.cfg, &bytes.Buffer{}); err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
		})
	}
}
