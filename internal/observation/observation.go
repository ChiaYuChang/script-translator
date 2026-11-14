package observation

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/google/uuid"
)

const DefaultLogLevel = "info"

// ConsoleLogConfig controls human-readable console logging.
type ConsoleLogConfig struct {
	Enable bool `json:"enable" jsonschema:"Write human-readable logs to console (stderr)"`
}

// FileLogConfig controls JSONL file logging.
type FileLogConfig struct {
	Enable bool   `json:"enable" jsonschema:"Write structured JSON logs to a file"`
	File   string `json:"file,omitzero" jsonschema:"Path to the JSONL log output file"`
}

// LogConfig configures console and file log handlers.
type LogConfig struct {
	Level   string           `json:"level,omitzero" jsonschema:"Minimum log level (debug, info, warn, error)"`
	Console ConsoleLogConfig `json:"console" jsonschema:"Console logging settings"`
	File    FileLogConfig    `json:"file" jsonschema:"File logging settings"`
}

// MetricConfig configures JSONL metric event output.
type MetricConfig struct {
	Enable bool   `json:"enable" jsonschema:"Write execution and token-usage metrics as JSONL events to a file"`
	File   string `json:"file,omitzero" jsonschema:"Path to the metrics JSONL output file"`
}

// Config configures logging and metrics output.
type Config struct {
	Log    LogConfig    `json:"log" jsonschema:"Logging configuration"`
	Metric MetricConfig `json:"metric" jsonschema:"Metrics configuration"`
}

// DefaultConfig returns default observation settings (console logging enabled,
// file logging and metric file output disabled).
func DefaultConfig() Config {
	return Config{
		Log: LogConfig{
			Level:   DefaultLogLevel,
			Console: ConsoleLogConfig{Enable: true},
		},
	}
}

// Normalize trims whitespace and applies empty defaults.
func (c *Config) Normalize() {
	c.Log.Level = strings.ToLower(strings.TrimSpace(c.Log.Level))
	if c.Log.Level == "" {
		c.Log.Level = DefaultLogLevel
	}
	c.Log.File.File = strings.TrimSpace(c.Log.File.File)
	c.Metric.File = strings.TrimSpace(c.Metric.File)
}

// Validate checks that log level and enabled output file paths are valid.
func (c Config) Validate() error {
	if _, err := parseLogLevel(c.Log.Level); err != nil {
		return err
	}
	if c.Log.File.Enable && c.Log.File.File == "" {
		return errors.New("observation.log.file.file must not be empty when file logging is enabled")
	}
	if c.Metric.Enable && c.Metric.File == "" {
		return errors.New("observation.metric.file must not be empty when metrics are enabled")
	}
	return nil
}

// Observer bundles a unique UUIDv7 RunID, configured logger, metrics
// collector, and flush/close lifecycle.
type Observer struct {
	RunID     string
	Logger    *slog.Logger
	Metrics   *Metrics
	closeOnce sync.Once
	closeErr  error
	closeFn   func() error
}

// Init constructs an Observer writing console logs (when enabled) to os.Stderr.
func Init(cfg Config) (*Observer, error) {
	return initWithWriter(cfg, os.Stderr)
}

// InitWithWriter constructs an Observer writing console logs (when enabled) to consoleOut.
func InitWithWriter(cfg Config, consoleOut io.Writer) (*Observer, error) {
	return initWithWriter(cfg, consoleOut)
}

func initWithWriter(cfg Config, consoleOut io.Writer) (*Observer, error) {
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("observation: %w", err)
	}

	runUUID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("observation: generate run id: %w", err)
	}
	runID := runUUID.String()

	logger, closeLog, err := NewLogger(cfg.Log, consoleOut)
	if err != nil {
		return nil, err
	}
	logger = logger.With(slog.String("run_id", runID))

	var metricFile *os.File
	var metricOut io.Writer
	if cfg.Metric.Enable {
		f, err := openAppendFile(cfg.Metric.File, "metric")
		if err != nil {
			if closeLog != nil {
				_ = closeLog()
			}
			return nil, err
		}
		metricFile = f
		metricOut = f
	}

	metrics := NewMetrics(runID, metricOut)

	return &Observer{
		RunID:   runID,
		Logger:  logger,
		Metrics: metrics,
		closeFn: func() error {
			var errs []error
			if err := metrics.WriteTotal(); err != nil {
				errs = append(errs, err)
			}
			if metricFile != nil {
				if err := metricFile.Close(); err != nil {
					errs = append(errs, fmt.Errorf("observation: close metric file: %w", err))
				}
			}
			if closeLog != nil {
				if err := closeLog(); err != nil {
					errs = append(errs, fmt.Errorf("observation: close log file: %w", err))
				}
			}
			return errors.Join(errs...)
		},
	}, nil
}

// Close writes the final total_usage metric event (when enabled) and closes any
// opened metric and log files. Repeated calls return the first result.
func (o *Observer) Close() error {
	if o == nil || o.closeFn == nil {
		return nil
	}
	o.closeOnce.Do(func() {
		o.closeErr = o.closeFn()
	})
	return o.closeErr
}

func openAppendFile(path, kind string) (*os.File, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("observation: create %s directory %q: %w", kind, dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("observation: open %s file %q: %w", kind, path, err)
	}
	return f, nil
}
