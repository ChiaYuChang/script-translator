package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
)

func TestParseAppliesDefaultsAndTrimsWhitespace(t *testing.T) {
	raw := []byte(`{
		"provider": {
			"base_url": " https://example.invalid/v1 "
		},
		"entity_recognition": {
			"model": " gpt-entity "
		},
		"translation": {
			"model": " gpt-translate "
		}
	}`)

	cfg, err := Parse(raw)
	if err != nil {
		t.Fatalf("Parse() unexpected error: %v", err)
	}

	if cfg.CacheDir != DefaultCacheDir {
		t.Fatalf("expected default cache_dir %q, got %q", DefaultCacheDir, cfg.CacheDir)
	}
	if cfg.Provider.BaseURL != "https://example.invalid/v1" {
		t.Fatalf("unexpected base_url: %q", cfg.Provider.BaseURL)
	}
	if cfg.Provider.APIKeyEnv != DefaultAPIKeyEnv {
		t.Fatalf("expected default api_key_env %q, got %q", DefaultAPIKeyEnv, cfg.Provider.APIKeyEnv)
	}
	if cfg.EntityRecognition.Model != "gpt-entity" {
		t.Fatalf("unexpected entity model: %q", cfg.EntityRecognition.Model)
	}
	if cfg.EntityRecognition.MaxAttempt != DefaultMaxAttempt {
		t.Fatalf("expected default recognition max_attempt %d, got %d", DefaultMaxAttempt, cfg.EntityRecognition.MaxAttempt)
	}
	if cfg.Translation.Model != "gpt-translate" {
		t.Fatalf("unexpected translation model: %q", cfg.Translation.Model)
	}
	if cfg.Translation.BatchSize != DefaultBatchSize {
		t.Fatalf("expected default batch_size %d, got %d", DefaultBatchSize, cfg.Translation.BatchSize)
	}
	if cfg.Translation.MaxAttempt != DefaultMaxAttempt {
		t.Fatalf("expected default translation max_attempt %d, got %d", DefaultMaxAttempt, cfg.Translation.MaxAttempt)
	}
	if !cfg.Observation.Log.Console.Enable || cfg.Observation.Log.File.Enable || cfg.Observation.Metric.Enable {
		t.Fatalf("unexpected default observation config: %#v", cfg.Observation)
	}
}

func TestLoadFromFileAndExplicitOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	content := `{
		"cache_dir": " /tmp/custom-cache ",
		"provider": {
			"api_key_env": "CUSTOM_LLM_KEY"
		},
		"entity_recognition": {
			"model": "model-a",
			"max_attempt": 2
		},
		"translation": {
			"model": "model-b",
			"batch_size": 15,
			"max_attempt": 4
		},
		"observation": {
			"log": {
				"level": "debug",
				"console": {"enable": false},
				"file": {"enable": true, "file": "logs/app.jsonl"}
			},
			"metric": {
				"enable": true,
				"file": "metrics/run.json"
			}
		}
	}`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() unexpected error: %v", err)
	}
	if cfg.CacheDir != "/tmp/custom-cache" ||
		cfg.Provider.APIKeyEnv != "CUSTOM_LLM_KEY" ||
		cfg.EntityRecognition.MaxAttempt != 2 ||
		cfg.Translation.BatchSize != 15 ||
		cfg.Translation.MaxAttempt != 4 ||
		cfg.Observation.Log.Level != "debug" ||
		cfg.Observation.Log.Console.Enable ||
		!cfg.Observation.Log.File.Enable ||
		cfg.Observation.Log.File.File != "logs/app.jsonl" ||
		!cfg.Observation.Metric.Enable ||
		cfg.Observation.Metric.File != "metrics/run.json" {
		t.Fatalf("unexpected loaded config: %#v", cfg)
	}
}

func TestLoadMissingFile(t *testing.T) {
	if _, err := Load(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestParseValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{name: "empty file", raw: "   \n"},
		{name: "invalid JSON", raw: "{"},
		{
			name: "unknown field rejected",
			raw:  `{"entity_recognition":{"model":"m"},"translation":{"model":"m","batchSize":10}}`,
		},
		{
			name: "dictionary rejected in config",
			raw:  `{"entity_recognition":{"model":"m","dictionary":[]},"translation":{"model":"m"}}`,
		},
		{
			name: "invalid base_url scheme",
			raw:  `{"provider":{"base_url":"ftp://example.com"},"entity_recognition":{"model":"m"},"translation":{"model":"m"}}`,
		},
		{
			name: "missing api_key and empty api_key_env",
			raw:  `{"provider":{"api_key_env":""},"entity_recognition":{"model":"m"},"translation":{"model":"m"}}`,
		},
		{
			name: "missing entity recognition model",
			raw:  `{"translation":{"model":"m"}}`,
		},
		{
			name: "explicit zero recognition max_attempt",
			raw:  `{"entity_recognition":{"model":"m","max_attempt":0},"translation":{"model":"m"}}`,
		},
		{
			name: "missing translation model",
			raw:  `{"entity_recognition":{"model":"m"}}`,
		},
		{
			name: "non-positive translation batch_size",
			raw:  `{"entity_recognition":{"model":"m"},"translation":{"model":"m","batch_size":0}}`,
		},
		{
			name: "negative translation max_attempt",
			raw:  `{"entity_recognition":{"model":"m"},"translation":{"model":"m","max_attempt":-1}}`,
		},
		{
			name: "empty cache_dir rejected",
			raw:  `{"cache_dir":"   ","entity_recognition":{"model":"m"},"translation":{"model":"m"}}`,
		},
		{
			name: "observation file log enabled without file path",
			raw:  `{"entity_recognition":{"model":"m"},"translation":{"model":"m"},"observation":{"log":{"file":{"enable":true}}}}`,
		},
		{
			name: "observation metric enabled without file path",
			raw:  `{"entity_recognition":{"model":"m"},"translation":{"model":"m"},"observation":{"metric":{"enable":true}}}`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse([]byte(tc.raw)); err == nil {
				t.Fatalf("expected Parse() error for %s, got nil", tc.name)
			}
		})
	}
}

func TestResolveAPIKey(t *testing.T) {
	pInline := ProviderConfig{APIKey: " sk-inline ", APIKeyEnv: "TEST_IGNORED_KEY"}
	key, err := pInline.ResolveAPIKey()
	if err != nil || key != "sk-inline" {
		t.Fatalf("expected inline key, got (%q, %v)", key, err)
	}

	t.Setenv("CUSTOM_OPENAI_KEY", " sk-from-env ")
	pEnv := ProviderConfig{APIKeyEnv: "CUSTOM_OPENAI_KEY"}
	key, err = pEnv.ResolveAPIKey()
	if err != nil || key != "sk-from-env" {
		t.Fatalf("expected env key, got (%q, %v)", key, err)
	}

	t.Setenv("EMPTY_OPENAI_KEY", " ")
	pEmpty := ProviderConfig{APIKeyEnv: "EMPTY_OPENAI_KEY"}
	if _, err := pEmpty.ResolveAPIKey(); err == nil {
		t.Fatal("expected error for blank env var")
	}
}

func TestProviderLogValueMasksAPIKey(t *testing.T) {
	p := ProviderConfig{
		BaseURL:   "https://example.invalid/v1",
		APIKey:    "sk-secret-value",
		APIKeyEnv: "OPENAI_API_KEY",
	}
	got := p.LogValue().String()
	if strings.Contains(got, "sk-secret-value") {
		t.Fatalf("LogValue leaked secret key: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("expected [REDACTED] in LogValue, got: %s", got)
	}

	// Ensure LogValue satisfies slog.LogValuer.
	var _ slog.LogValuer = p
}

func TestConfigSchemaDefaultsAndDescriptions(t *testing.T) {
	schema, err := Schema()
	if err != nil {
		t.Fatalf("Schema() unexpected error: %v", err)
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		t.Fatalf("schema.Resolve() unexpected error: %v", err)
	}

	// Minimal instance omitting provider, max_attempt, and batch_size.
	instance := map[string]any{
		"entity_recognition": map[string]any{"model": "gpt-entity"},
		"translation":        map[string]any{"model": "gpt-translate"},
	}
	if err := resolved.Validate(instance); err != nil {
		t.Fatalf("Validate() minimal config error: %v", err)
	}

	cfg, err := Parse([]byte(`{
		"entity_recognition": {"model": "gpt-entity"},
		"translation": {"model": "gpt-translate"}
	}`))
	if err != nil {
		t.Fatalf("Parse() omitting provider failed: %v", err)
	}
	if cfg.Provider.APIKeyEnv != DefaultAPIKeyEnv ||
		cfg.EntityRecognition.MaxAttempt != DefaultMaxAttempt ||
		cfg.Translation.BatchSize != DefaultBatchSize ||
		cfg.Translation.MaxAttempt != DefaultMaxAttempt {
		t.Fatalf("unexpected defaulted config: %#v", cfg)
	}

	if schema.Properties["provider"].Description == "" {
		t.Fatal("expected jsonschema description tag to populate provider description")
	}
}
