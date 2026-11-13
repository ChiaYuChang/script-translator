package observation

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("invalid log level %q (want debug, info, warn, or error)", raw)
	}
}

// NewLogger builds a *slog.Logger writing text logs to consoleOut and/or JSON
// lines to cfg.File.File according to cfg. When both targets are disabled, the
// returned logger discards all records.
func NewLogger(cfg LogConfig, consoleOut io.Writer) (*slog.Logger, func() error, error) {
	level, err := parseLogLevel(cfg.Level)
	if err != nil {
		return nil, nil, fmt.Errorf("observation: %w", err)
	}
	opts := &slog.HandlerOptions{Level: level}

	var handlers []slog.Handler
	if cfg.Console.Enable && consoleOut != nil {
		handlers = append(handlers, slog.NewTextHandler(consoleOut, opts))
	}

	var logFile *os.File
	if cfg.File.Enable {
		path := strings.TrimSpace(cfg.File.File)
		if path == "" {
			return nil, nil, fmt.Errorf("observation: empty log file path")
		}
		f, err := openAppendFile(path, "log")
		if err != nil {
			return nil, nil, err
		}
		logFile = f
		handlers = append(handlers, slog.NewJSONHandler(f, opts))
	}

	var handler slog.Handler
	switch len(handlers) {
	case 0:
		handler = slog.DiscardHandler
	case 1:
		handler = handlers[0]
	default:
		handler = slog.NewMultiHandler(handlers...)
	}

	closeFn := func() error {
		if logFile != nil {
			return logFile.Close()
		}
		return nil
	}
	return slog.New(handler), closeFn, nil
}
