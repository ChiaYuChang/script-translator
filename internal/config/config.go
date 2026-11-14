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
	BaseURL   string `json:"base_url,omitzero" jsonschema:"Optional OpenAI-compatible API base URL"`
	APIKey    string `json:"api_key,omitzero" jsonschema:"Optional inline API key overriding api_key_env"`
	APIKeyEnv string `json:"api_key_env,omitzero" jsonschema:"Environment variable name holding the API key"`
	WaitTime  int    `json:"wait_time,omitzero" jsonschema:"Seconds to wait between LLM calls/batches (0 disables pacing)"`
	RetryTime int    `json:"retry_time,omitzero" jsonschema:"Base seconds for exponential backoff on 429/5xx retries (0 disables backoff)"`
}

// ResolveAPIKey returns the inline APIKey when configured, otherwise reading the
// environment variable named by APIKeyEnv.
func (p ProviderConfig) ResolveAPIKey() (string, error) {
	if key := strings.TrimSpace(p.APIKey); key != "" {
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
		slog.String("api_key_env", p.APIKeyEnv),
	)
}

// Config holds application settings aligned with the llm package parameter types.
type Config struct {
	CacheDir          string                      `json:"cache_dir,omitzero" jsonschema:"Directory path for cached intermediate script artifacts"`
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

	// Allow omitting "provider" and "observation" so defaults fill nested settings.
	schema.Required = withoutStrings(schema.Required, "provider", "observation")
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
	data, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("config: read %q: %w", path, err)
	}
	cfg, err := Parse(data)
	if err != nil {
		return Config{}, fmt.Errorf("config: parse %q: %w", path, err)
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
	if err := json.Unmarshal(data, &cfg, json.RejectUnknownMembers(true)); err != nil {
		return Config{}, err
	}

	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c *Config) normalize() {
	c.CacheDir = strings.TrimSpace(c.CacheDir)
	c.Provider.BaseURL = strings.TrimSpace(c.Provider.BaseURL)
	c.Provider.APIKey = strings.TrimSpace(c.Provider.APIKey)
	c.Provider.APIKeyEnv = strings.TrimSpace(c.Provider.APIKeyEnv)
	c.EntityRecognition.Model = strings.TrimSpace(c.EntityRecognition.Model)
	c.Translation.Model = strings.TrimSpace(c.Translation.Model)
	c.Observation.Normalize()
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
	if c.Provider.APIKey == "" && c.Provider.APIKeyEnv == "" {
		return errors.New("provider requires api_key or api_key_env")
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

	if c.Translation.Model == "" {
		return errors.New("translation.model must not be empty")
	}
	if c.Translation.BatchSize <= 0 {
		return fmt.Errorf("translation.batch_size must be positive (got %d)", c.Translation.BatchSize)
	}
	if c.Translation.MaxAttempt <= 0 {
		return fmt.Errorf("translation.max_attempt must be positive (got %d)", c.Translation.MaxAttempt)
	}

	if err := c.Observation.Validate(); err != nil {
		return err
	}
	return nil
}
