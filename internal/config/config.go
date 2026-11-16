package config

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"

	"go-script-translate/internal/llm"
	"go-script-translate/internal/observation"
	"go-script-translate/internal/pathutil"
	"go-script-translate/internal/prompt"
)

const (
	DefaultCacheDir   = "./cache"
	DefaultAPIKeyEnv  = "OPENAI_API_KEY"
	DefaultBatchSize  = 20
	DefaultMaxAttempt = 3
	DefaultWaitTime   = 0
	DefaultRetryTime  = 0
)

// ProviderConfig defines connection and credential settings for an
// OpenAI-compatible Responses API endpoint.
type ProviderConfig struct {
	BaseURL    string `json:"base_url,omitzero" jsonschema:"Optional OpenAI-compatible API base URL"`
	APIKey     string `json:"api_key,omitzero" jsonschema:"Optional inline API key overriding api_key_file and api_key_env"`
	APIKeyFile string `json:"api_key_file,omitzero" jsonschema:"Optional file path holding the API key (supports ~ home expansion, overrides api_key_env)"`
	APIKeyEnv  string `json:"api_key_env,omitzero" jsonschema:"Environment variable name holding the API key"`
	WaitTime   int    `json:"wait_time,omitzero" jsonschema:"Seconds to wait between LLM calls/batches (0 disables pacing)"`
	RetryTime  int    `json:"retry_time,omitzero" jsonschema:"Base seconds for exponential backoff on 429/5xx retries (0 disables backoff)"`
}

// ResolveAPIKey resolves the API key using precedence:
// 1. inline APIKey
// 2. file path in APIKeyFile (supports ~ home expansion)
// 3. environment variable named by APIKeyEnv
func (p ProviderConfig) ResolveAPIKey() (string, error) {
	if key := strings.TrimSpace(p.APIKey); key != "" {
		return key, nil
	}
	if filePath := strings.TrimSpace(p.APIKeyFile); filePath != "" {
		resolvedPath, err := pathutil.ExpandHome(filePath)
		if err != nil {
			return "", fmt.Errorf("config: resolve api_key_file %q: %w", filePath, err)
		}
		data, err := os.ReadFile(resolvedPath) // #nosec G304 -- api_key_file path is explicitly configured by user
		if err != nil {
			return "", fmt.Errorf("config: read api_key_file %q: %w", resolvedPath, err)
		}
		key := strings.TrimSpace(string(data))
		if key == "" {
			return "", fmt.Errorf("config: api_key_file %q is empty", resolvedPath)
		}
		return key, nil
	}
	envName := strings.TrimSpace(p.APIKeyEnv)
	if envName == "" {
		return "", errors.New("config: empty api_key_env")
	}
	key := strings.TrimSpace(os.Getenv(envName))
	if key == "" {
		return "", fmt.Errorf("config: environment variable %q is empty or unset", envName)
	}
	return key, nil
}

// LogValue masks APIKey so structured logs never leak credentials.
func (p ProviderConfig) LogValue() slog.Value {
	apiKey := ""
	if strings.TrimSpace(p.APIKey) != "" {
		apiKey = "[REDACTED]"
	}
	return slog.GroupValue(
		slog.String("base_url", p.BaseURL),
		slog.String("api_key", apiKey),
		slog.String("api_key_file", p.APIKeyFile),
		slog.String("api_key_env", p.APIKeyEnv),
	)
}

// Config holds application settings aligned with the llm package parameter types.
type Config struct {
	CacheDir          string                      `json:"cache_dir,omitzero" jsonschema:"Directory path for cached intermediate script artifacts"`
	Language          prompt.Language             `json:"language,omitzero" jsonschema:"Source/target localization language and optional regional locale"`
	Provider          ProviderConfig              `json:"provider" jsonschema:"OpenAI-compatible provider connection settings"`
	EntityRecognition llm.RecognizeEntitiesParams `json:"entity_recognition" jsonschema:"Entity recognition step parameters"`
	Translation       llm.TranslateParams         `json:"translation" jsonschema:"Translation step parameters"`
	Observation       observation.Config          `json:"observation" jsonschema:"Logging and metrics output settings"`
}

// Default returns a Config populated with default operational settings.
// Model names remain empty and must be supplied explicitly.
func Default() Config {
	return Config{
		CacheDir: DefaultCacheDir,
		Language: prompt.DefaultLanguage(),
		Provider: ProviderConfig{
			APIKeyEnv: DefaultAPIKeyEnv,
			WaitTime:  DefaultWaitTime,
			RetryTime: DefaultRetryTime,
		},
		EntityRecognition: llm.RecognizeEntitiesParams{
			MaxAttempt: DefaultMaxAttempt,
		},
		Translation: llm.TranslateParams{
			BatchSize:  DefaultBatchSize,
			MaxAttempt: DefaultMaxAttempt,
		},
		Observation: observation.DefaultConfig(),
	}
}

// Schema builds the JSON Schema for Config, including defaults and constraints
// applied before decoding into Go structs.
func Schema() (*jsonschema.Schema, error) {
	schema, err := jsonschema.For[Config](nil)
	if err != nil {
		return nil, fmt.Errorf("config: build schema: %w", err)
	}

	minZeroFloat := 0.0
	minOneFloat := 1.0
	minOneInt := 1

	// Allow omitting "provider", "observation", and "language" so defaults fill nested settings.
	schema.Required = withoutStrings(schema.Required, "provider", "observation", "language")
	if cacheProp := schema.Properties["cache_dir"]; cacheProp != nil {
		b, err := json.Marshal(DefaultCacheDir)
		if err != nil {
			return nil, fmt.Errorf("config: marshal default cache_dir: %w", err)
		}
		cacheProp.Default = b
	}
	if prov := schema.Properties["provider"]; prov != nil {
		if apiKeyEnv := prov.Properties["api_key_env"]; apiKeyEnv != nil {
			b, err := json.Marshal(DefaultAPIKeyEnv)
			if err != nil {
				return nil, fmt.Errorf("config: marshal default api_key_env: %w", err)
			}
			apiKeyEnv.Default = b
		}
		if waitProp := prov.Properties["wait_time"]; waitProp != nil {
			b, err := json.Marshal(DefaultWaitTime)
			if err != nil {
				return nil, fmt.Errorf("config: marshal default wait_time: %w", err)
			}
			waitProp.Default = b
			waitProp.Minimum = &minZeroFloat
		}
		if retryProp := prov.Properties["retry_time"]; retryProp != nil {
			b, err := json.Marshal(DefaultRetryTime)
			if err != nil {
				return nil, fmt.Errorf("config: marshal default retry_time: %w", err)
			}
			retryProp.Default = b
			retryProp.Minimum = &minZeroFloat
		}
	}

	if obs := schema.Properties["observation"]; obs != nil {
		obs.Required = withoutStrings(obs.Required, "log", "metric")
		if logSchema := obs.Properties["log"]; logSchema != nil {
			logSchema.Required = withoutStrings(logSchema.Required, "console", "file")
			if consoleSchema := logSchema.Properties["console"]; consoleSchema != nil {
				consoleSchema.Required = withoutStrings(consoleSchema.Required, "enable")
			}
			if fileSchema := logSchema.Properties["file"]; fileSchema != nil {
				fileSchema.Required = withoutStrings(fileSchema.Required, "enable")
			}
		}
		if metricSchema := obs.Properties["metric"]; metricSchema != nil {
			metricSchema.Required = withoutStrings(metricSchema.Required, "enable")
		}
	}

	if rec := schema.Properties["entity_recognition"]; rec != nil {
		rec.Required = withoutStrings(rec.Required, "max_attempt")
		if modelProp := rec.Properties["model"]; modelProp != nil {
			modelProp.MinLength = &minOneInt
		}
		if maxAttempt := rec.Properties["max_attempt"]; maxAttempt != nil {
			b, err := json.Marshal(DefaultMaxAttempt)
			if err != nil {
				return nil, fmt.Errorf("config: marshal default max_attempt: %w", err)
			}
			maxAttempt.Default = b
			maxAttempt.Minimum = &minOneFloat
		}
	}

	if tr := schema.Properties["translation"]; tr != nil {
		tr.Required = withoutStrings(tr.Required, "batch_size", "max_attempt")
		if modelProp := tr.Properties["model"]; modelProp != nil {
			modelProp.MinLength = &minOneInt
		}
		if batchSize := tr.Properties["batch_size"]; batchSize != nil {
			b, err := json.Marshal(DefaultBatchSize)
			if err != nil {
				return nil, fmt.Errorf("config: marshal default batch_size: %w", err)
			}
			batchSize.Default = b
			batchSize.Minimum = &minOneFloat
		}
		if maxAttempt := tr.Properties["max_attempt"]; maxAttempt != nil {
			b, err := json.Marshal(DefaultMaxAttempt)
			if err != nil {
				return nil, fmt.Errorf("config: marshal default max_attempt: %w", err)
			}
			maxAttempt.Default = b
			maxAttempt.Minimum = &minOneFloat
		}
	}

	return schema, nil
}

func withoutStrings(list []string, exclude ...string) []string {
	if len(list) == 0 {
		return nil
	}
	out := make([]string, 0, len(list))
	for _, s := range list {
		if !slices.Contains(exclude, s) {
			out = append(out, s)
		}
	}
	return out
}

// Load reads and validates a JSON configuration file at path.
func Load(path string) (Config, error) {
	resolvedPath, err := pathutil.ExpandHome(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: resolve %q: %w", path, err)
	}
	data, err := os.ReadFile(resolvedPath) // #nosec G304 -- config path is explicitly selected by CLI/user
	if err != nil {
		return Config{}, fmt.Errorf("config: read %q: %w", resolvedPath, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("config: parse %q: %w", resolvedPath, err)
	}
	return cfg, nil
}

// Parse unmarshals JSON configuration bytes, validates against the JSON Schema,
// applies defaults for omitted fields, and validates semantic constraints.
func Parse(data []byte) (Config, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return Config{}, errors.New("config: empty JSON")
	}

	var instance any
	if err := json.Unmarshal(data, &instance); err != nil {
		return Config{}, err
	}

	schema, err := Schema()
	if err != nil {
		return Config{}, err
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		return Config{}, fmt.Errorf("config: resolve schema: %w", err)
	}
	if err := resolved.Validate(instance); err != nil {
		return Config{}, fmt.Errorf("config: validate schema: %w", err)
	}

	// Unmarshal onto Default() rather than relying on Resolved.ApplyDefaults:
	// jsonschema-go skips recursing into required object properties such as
	// entity_recognition and translation when applying defaults.
	cfg := Default()
	if obj, ok := instance.(map[string]any); ok {
		if _, hasLang := obj["language"]; hasLang {
			// Clear default locale so specifying e.g. {"to": "English"}
			// without "location" does not inherit "Taiwan (zh-TW)".
			cfg.Language = prompt.Language{}
		}
	}
	if err := json.Unmarshal(data, &cfg, json.RejectUnknownMembers(true)); err != nil {
		return Config{}, err
	}

	cfg.normalize()
	if err := cfg.resolvePaths(); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) normalize() {
	c.CacheDir = strings.TrimSpace(c.CacheDir)
	c.Language.Normalize()
	c.Provider.BaseURL = strings.TrimSpace(c.Provider.BaseURL)
	c.Provider.APIKey = strings.TrimSpace(c.Provider.APIKey)
	c.Provider.APIKeyFile = strings.TrimSpace(c.Provider.APIKeyFile)
	c.Provider.APIKeyEnv = strings.TrimSpace(c.Provider.APIKeyEnv)
	c.EntityRecognition.Model = strings.TrimSpace(c.EntityRecognition.Model)
	c.EntityRecognition.ExampleFile = strings.TrimSpace(c.EntityRecognition.ExampleFile)
	c.Translation.Model = strings.TrimSpace(c.Translation.Model)
	c.Translation.ExampleFile = strings.TrimSpace(c.Translation.ExampleFile)
	c.Observation.Normalize()
}

func (c *Config) resolvePaths() error {
	cacheDir, err := pathutil.ExpandHome(c.CacheDir)
	if err != nil {
		return fmt.Errorf("config: cache_dir: %w", err)
	}
	c.CacheDir = cacheDir
	if c.Provider.APIKeyFile != "" {
		keyFile, err := pathutil.ExpandHome(c.Provider.APIKeyFile)
		if err != nil {
			return fmt.Errorf("config: provider.api_key_file: %w", err)
		}
		c.Provider.APIKeyFile = keyFile
	}
	if err := resolveExampleFile("entity_recognition.example_file", &c.EntityRecognition.ExampleFile, &c.EntityRecognition.Example); err != nil {
		return err
	}
	if err := resolveExampleFile("translation.example_file", &c.Translation.ExampleFile, &c.Translation.Example); err != nil {
		return err
	}
	return c.Observation.ResolvePaths()
}

func resolveExampleFile[T any](field string, pathPtr *string, dst **T) error {
	if *pathPtr == "" {
		return nil
	}
	resolved, err := pathutil.ExpandHome(*pathPtr)
	if err != nil {
		return fmt.Errorf("config: %s: %w", field, err)
	}
	*pathPtr = resolved

	data, err := os.ReadFile(resolved) // #nosec G304 -- example_file path is explicitly configured by CLI/user
	if err != nil {
		return fmt.Errorf("config: %s %q: %w", field, resolved, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return fmt.Errorf("config: %s %q: empty JSON", field, resolved)
	}
	var parsed T
	if err := json.Unmarshal(data, &parsed, json.RejectUnknownMembers(true)); err != nil {
		return fmt.Errorf("config: parse %s %q: %w", field, resolved, err)
	}
	*dst = &parsed
	return nil
}

// Validate checks semantic constraints not expressible in structural JSON Schema
// (such as URL syntax and non-whitespace strings).
func (c Config) Validate() error {
	if c.CacheDir == "" {
		return errors.New("cache_dir must not be empty")
	}
	if c.Provider.BaseURL != "" {
		u, err := url.Parse(c.Provider.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("invalid provider.base_url %q", c.Provider.BaseURL)
		}
	}
	if c.Provider.APIKey == "" && c.Provider.APIKeyFile == "" && c.Provider.APIKeyEnv == "" {
		return errors.New("provider requires api_key, api_key_file, or api_key_env")
	}
	if c.Provider.WaitTime < 0 {
		return fmt.Errorf("provider.wait_time must be non-negative (got %d)", c.Provider.WaitTime)
	}
	if c.Provider.RetryTime < 0 {
		return fmt.Errorf("provider.retry_time must be non-negative (got %d)", c.Provider.RetryTime)
	}

	if c.EntityRecognition.Model == "" {
		return errors.New("entity_recognition.model must not be empty")
	}
	if c.EntityRecognition.MaxAttempt <= 0 {
		return fmt.Errorf("entity_recognition.max_attempt must be positive (got %d)", c.EntityRecognition.MaxAttempt)
	}
	if c.EntityRecognition.Example != nil {
		if err := c.EntityRecognition.Example.Validate(); err != nil {
			return fmt.Errorf("entity_recognition.example: %w", err)
		}
	}

	if c.Translation.Model == "" {
		return errors.New("translation.model must not be empty")
	}
	if c.Translation.BatchSize <= 0 {
		return fmt.Errorf("translation.batch_size must be positive (got %d)", c.Translation.BatchSize)
	}
	if c.Translation.MaxAttempt <= 0 {
		return fmt.Errorf("translation.max_attempt must be positive (got %d)", c.Translation.MaxAttempt)
	}
	if c.Translation.Example != nil {
		if err := c.Translation.Example.Validate(); err != nil {
			return fmt.Errorf("translation.example: %w", err)
		}
	}

	if err := c.Observation.Validate(); err != nil {
		return err
	}
	return nil
}
