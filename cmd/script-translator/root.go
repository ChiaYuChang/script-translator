package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/spf13/cobra"

	"go-script-translate/internal/config"
	"go-script-translate/internal/llm"
	"go-script-translate/internal/model"
	"go-script-translate/internal/observation"
	"go-script-translate/internal/pathutil"
)

const (
	appName             = "script-translator"
	defaultInputPath    = "script.txt"
	defaultOutputPath   = "translated.txt"
	configFlagUsage     = "Path to config JSON file (default: ~/.config/script-translator/config.json)"
	localConfigFilename = "config.json"
)

// version is overridden at build time via `-ldflags "-X main.version=..."`.
var version = "dev"

// recognizePayload is the JSON schema exchanged between `script recognize`,
// `script translate`, and `entity insert` over files or Unix pipes.
type recognizePayload struct {
	Input    string         `json:"input,omitzero"`
	Hashsum  string         `json:"hashsum,omitzero"`
	Entities []model.Entity `json:"entities"`
}

type cliDeps struct {
	stdin         io.Reader
	stdout        io.Writer
	stderr        io.Writer
	stdinIsTTY    func() bool
	userConfigDir func() (string, error)
}

func defaultCLIDeps() cliDeps {
	return cliDeps{
		stdin:  os.Stdin,
		stdout: os.Stdout,
		stderr: os.Stderr,
		stdinIsTTY: func() bool {
			info, err := os.Stdin.Stat()
			if err != nil {
				return true
			}
			return info.Mode()&os.ModeCharDevice != 0
		},
		userConfigDir: os.UserConfigDir,
	}
}

func newRootCmd(deps cliDeps) *cobra.Command {
	root := &cobra.Command{
		Use:           appName,
		Short:         "Multi-language visual novel/game script localization CLI",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.SetIn(deps.stdin)
	root.SetOut(deps.stdout)
	root.SetErr(deps.stderr)

	root.AddCommand(newScriptCmd(deps))
	root.AddCommand(newEntityCmd(deps))
	root.AddCommand(newDefaultCmd(deps))
	root.AddCommand(newVersionCmd(deps))
	return root
}

func newVersionCmd(deps cliDeps) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print CLI version",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, err := fmt.Fprintf(deps.stdout, "%s version %s\n", appName, version)
			return err
		},
	}
}

func resolveConfigPath(explicit string, deps cliDeps) (string, error) {
	if p := strings.TrimSpace(explicit); p != "" {
		return pathutil.ExpandHome(p)
	}
	getDir := deps.userConfigDir
	if getDir == nil {
		getDir = os.UserConfigDir
	}
	dir, err := getDir()
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	resolvedDir, err := pathutil.ExpandHome(dir)
	if err != nil {
		return "", fmt.Errorf("resolve user config directory: %w", err)
	}
	return filepath.Join(resolvedDir, appName, localConfigFilename), nil
}

func initConfigAndObserver(configPath string, deps cliDeps) (config.Config, *observation.Observer, error) {
	resolvedConfigPath, err := resolveConfigPath(configPath, deps)
	if err != nil {
		return config.Config{}, nil, err
	}
	cfg, err := config.Load(resolvedConfigPath)
	if err != nil {
		return config.Config{}, nil, err
	}

	obs, err := observation.InitWithWriter(cfg.Observation, deps.stderr)
	if err != nil {
		return config.Config{}, nil, err
	}
	return cfg, obs, nil
}

func initProvider(cfg config.Config, saveRawDir, rawPrefix string, obs *observation.Observer) (*llm.Provider, error) {
	apiKey, err := cfg.Provider.ResolveAPIKey()
	if err != nil {
		return nil, err
	}

	opts := []option.RequestOption{
		option.WithAPIKey(apiKey),
		option.WithMaxRetries(0),
	}
	if cfg.Provider.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.Provider.BaseURL))
	}
	if dir := strings.TrimSpace(saveRawDir); dir != "" {
		resolvedRawDir, err := pathutil.ExpandHome(dir)
		if err != nil {
			return nil, fmt.Errorf("resolve --save-raw directory: %w", err)
		}
		opts = append(opts, rawResponseMiddleware(resolvedRawDir, rawPrefix, obs))
	}
	client := openai.NewClient(opts...)
	provider := llm.NewProvider(
		&client,
		llm.WithLogger(obs.Logger),
		llm.WithMetrics(obs.Metrics),
		llm.WithWaitTime(time.Duration(cfg.Provider.WaitTime)*time.Second),
		llm.WithRetryTime(time.Duration(cfg.Provider.RetryTime)*time.Second),
	)
	return provider, nil
}

func initObserverAndProvider(configPath, saveRawDir, rawPrefix string, deps cliDeps) (config.Config, *observation.Observer, *llm.Provider, error) {
	cfg, obs, err := initConfigAndObserver(configPath, deps)
	if err != nil {
		return config.Config{}, nil, nil, err
	}
	provider, err := initProvider(cfg, saveRawDir, rawPrefix, obs)
	if err != nil {
		_ = obs.Close()
		return config.Config{}, nil, nil, err
	}
	return cfg, obs, provider, nil
}

func rawResponseMiddleware(dir, prefix string, obs *observation.Observer) option.RequestOption {
	var seq atomic.Int32
	return option.WithMiddleware(func(req *http.Request, next option.MiddlewareNext) (*http.Response, error) {
		resp, err := next(req)
		if err != nil || resp == nil || resp.Body == nil {
			return resp, err
		}
		body, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		resp.Body = io.NopCloser(bytes.NewReader(body))
		if readErr != nil {
			obs.Logger.Warn("failed to read raw response body", "error", readErr)
			return resp, err
		}

		trimmed := bytes.TrimSpace(body)
		if len(trimmed) == 0 {
			return resp, err
		}

		n := seq.Add(1)
		name := fmt.Sprintf("%s_%03d.json", prefix, n)
		if resp.StatusCode >= 300 {
			name = fmt.Sprintf("%s_%03d_err_%d.json", prefix, n, resp.StatusCode)
		}
		path := filepath.Join(dir, name)
		data := append(append([]byte(nil), trimmed...), '\n')
		if writeErr := writeFileAtomic(path, data); writeErr != nil {
			obs.Logger.Warn("failed to save raw response", "path", path, "error", writeErr)
		}
		return resp, err
	})
}

func readInputBytes(path string, stdin io.Reader) ([]byte, error) {
	p, err := pathutil.ExpandHome(path)
	if err != nil {
		return nil, fmt.Errorf("resolve input path %q: %w", path, err)
	}
	if p == "" || p == "-" {
		data, err := io.ReadAll(stdin)
		if err != nil {
			return nil, fmt.Errorf("read stdin: %w", err)
		}
		return data, nil
	}
	data, err := os.ReadFile(p) // #nosec G304 -- input path is explicitly provided by CLI/user
	if err != nil {
		return nil, fmt.Errorf("read %q: %w", p, err)
	}
	return data, nil
}

func decodeRecognizePayload(data []byte) (recognizePayload, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return recognizePayload{}, errors.New("empty entities JSON input")
	}
	var payload recognizePayload
	if err := json.Unmarshal(data, &payload); err != nil {
		return recognizePayload{}, fmt.Errorf("parse entities JSON: %w", err)
	}
	if payload.Entities == nil {
		return recognizePayload{}, errors.New("entities JSON missing required \"entities\" array")
	}
	return payload, nil
}

func writeOutputBytes(path string, data []byte, stdout io.Writer) error {
	p, err := pathutil.ExpandHome(path)
	if err != nil {
		return fmt.Errorf("resolve output path %q: %w", path, err)
	}
	if p == "" || p == "-" {
		if _, err := stdout.Write(data); err != nil {
			return fmt.Errorf("write stdout: %w", err)
		}
		return nil
	}
	return writeFileAtomic(p, data)
}

func writeFileAtomic(path string, data []byte) error {
	resolvedPath, err := pathutil.ExpandHome(path)
	if err != nil {
		return fmt.Errorf("resolve output path %q: %w", path, err)
	}
	path = resolvedPath
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory %q: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".out-*.tmp")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename output file to %q: %w", path, err)
	}
	cleanup = false
	return nil
}

func marshalIndentedLine(v any) ([]byte, error) {
	var buf bytes.Buffer
	if err := json.MarshalWrite(&buf, v, jsontext.WithIndent("  ")); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}
