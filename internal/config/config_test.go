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
			"api_key_env": "CUSTOM_LLM_KEY",
			"wait_time": 2,
			"retry_time": 5
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
		cfg.Provider.WaitTime != 2 ||
		cfg.Provider.RetryTime != 5 ||
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
			name: "negative provider wait_time rejected",
			raw:  `{"provider":{"wait_time":-1},"entity_recognition":{"model":"m"},"translation":{"model":"m"}}`,
		},
		{
			name: "negative provider retry_time rejected",
			raw:  `{"provider":{"retry_time":-2},"entity_recognition":{"model":"m"},"translation":{"model":"m"}}`,
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
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	keyFile := filepath.Join(fakeHome, "api.key")
	if err := os.WriteFile(keyFile, []byte(" sk-from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CUSTOM_OPENAI_KEY", " sk-from-env ")

	// Precedence: inline > api_key_file > api_key_env.
	pInline := ProviderConfig{APIKey: " sk-inline ", APIKeyFile: keyFile, APIKeyEnv: "CUSTOM_OPENAI_KEY"}
	key, err := pInline.ResolveAPIKey()
	if err != nil || key != "sk-inline" {
		t.Fatalf("expected inline key, got (%q, %v)", key, err)
	}

	pFile := ProviderConfig{APIKeyFile: "~/api.key", APIKeyEnv: "CUSTOM_OPENAI_KEY"}
	key, err = pFile.ResolveAPIKey()
	if err != nil || key != "sk-from-file" {
		t.Fatalf("expected file key overriding env, got (%q, %v)", key, err)
	}

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

	// Explicit api_key_file errors must not silently fall back to api_key_env.
	pMissingFile := ProviderConfig{APIKeyFile: filepath.Join(fakeHome, "missing.key"), APIKeyEnv: "CUSTOM_OPENAI_KEY"}
	if _, err := pMissingFile.ResolveAPIKey(); err == nil {
		t.Fatal("expected missing api_key_file to fail without falling back to env")
	}

	emptyKeyFile := filepath.Join(fakeHome, "empty.key")
	if err := os.WriteFile(emptyKeyFile, []byte("  \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pBlankFile := ProviderConfig{APIKeyFile: emptyKeyFile, APIKeyEnv: "CUSTOM_OPENAI_KEY"}
	if _, err := pBlankFile.ResolveAPIKey(); err == nil {
		t.Fatal("expected blank api_key_file to fail without falling back to env")
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

func TestConfigExpandsTildePaths(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	cfgDir := filepath.Join(fakeHome, ".config", "script-translator")
	if err := os.MkdirAll(cfgDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfgRaw := `{
		"cache_dir": "~/.config/script-translator/cache",
		"provider": {"api_key_file": "~/.config/script-translator/api_key", "api_key_env": ""},
		"entity_recognition": {"model": "gpt-entity"},
		"translation": {"model": "gpt-translate"},
		"observation": {
			"log": {
				"level": "info",
				"console": {"enable": false},
				"file": {"enable": true, "file": "~/.config/script-translator/run.jsonl"}
			},
			"metric": {"enable": true, "file": "~/.config/script-translator/usage.jsonl"}
		}
	}`
	if err := os.WriteFile(filepath.Join(cfgDir, "config.json"), []byte(cfgRaw), 0o600); err != nil {
		t.Fatal(err)
	}

	loaded, err := Load("~/.config/script-translator/config.json")
	if err != nil {
		t.Fatalf("Load with tilde path failed: %v", err)
	}
	wantCache := filepath.Join(fakeHome, ".config", "script-translator", "cache")
	wantKeyFile := filepath.Join(fakeHome, ".config", "script-translator", "api_key")
	wantLog := filepath.Join(fakeHome, ".config", "script-translator", "run.jsonl")
	wantMetric := filepath.Join(fakeHome, ".config", "script-translator", "usage.jsonl")
	if loaded.CacheDir != wantCache {
		t.Fatalf("CacheDir = %q, want %q", loaded.CacheDir, wantCache)
	}
	if loaded.Provider.APIKeyFile != wantKeyFile {
		t.Fatalf("Provider.APIKeyFile = %q, want %q", loaded.Provider.APIKeyFile, wantKeyFile)
	}
	if loaded.Observation.Log.File.File != wantLog {
		t.Fatalf("Log.File.File = %q, want %q", loaded.Observation.Log.File.File, wantLog)
	}
	if loaded.Observation.Metric.File != wantMetric {
		t.Fatalf("Metric.File = %q, want %q", loaded.Observation.Metric.File, wantMetric)
	}
}

func TestParseLanguageAndPromptExampleFiles(t *testing.T) {
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	// Omitting "language" applies full Japanese -> Traditional Chinese (Taiwan) defaults.
	defaultCfg, err := Parse([]byte(`{
		"entity_recognition": {"model": "gpt-entity"},
		"translation": {"model": "gpt-translate"}
	}`))
	if err != nil {
		t.Fatalf("Parse default failed: %v", err)
	}
	if defaultCfg.Language.From != "Japanese" ||
		defaultCfg.Language.To != "Traditional Chinese" ||
		defaultCfg.Language.Location != "Taiwan (zh-TW)" {
		t.Fatalf("unexpected default language: %#v", defaultCfg.Language)
	}

	erFile := filepath.Join(fakeHome, "er_example.json")
	if err := os.WriteFile(erFile, []byte(`{
		"input": {
			"transcript": [{"speaker": "지수", "text": "서울에 가자."}]
		},
		"output": {
			"entity": [{"canonical": {"from": "서울", "to": "Seoul"}}]
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	trFile := filepath.Join(fakeHome, "tr_example.json")
	if err := os.WriteFile(trFile, []byte(`{
		"input": {
			"entries": [{"canonical": {"from": "지수", "to": "Jisoo"}}],
			"dialogue": [{"index": 0, "text": "안녕."}]
		},
		"output": {
			"dialogue": [{"index": 0, "text": "Hey."}]
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	// Custom "language" without "location" leaves Location empty (does not inject Taiwan).
	customCfg, err := Parse([]byte(`{
		"language": {"from": " Korean ", "to": " English "},
		"entity_recognition": {
			"model": "gpt-entity",
			"example_file": "~/er_example.json"
		},
		"translation": {
			"model": "gpt-translate",
			"example_file": "~/tr_example.json"
		}
	}`))
	if err != nil {
		t.Fatalf("Parse custom language/example_file failed: %v", err)
	}
	if customCfg.Language.From != "Korean" || customCfg.Language.To != "English" || customCfg.Language.Location != "" {
		t.Fatalf("unexpected custom language: %#v", customCfg.Language)
	}
	if customCfg.EntityRecognition.ExampleFile != erFile || customCfg.EntityRecognition.Example == nil {
		t.Fatalf("expected expanded and loaded ER example, got %#v", customCfg.EntityRecognition)
	}
	if customCfg.Translation.ExampleFile != trFile || customCfg.Translation.Example == nil {
		t.Fatalf("expected expanded and loaded translation example, got %#v", customCfg.Translation)
	}

	// Inline "example" is rejected as unknown member.
	if _, err := Parse([]byte(`{
		"entity_recognition": {"model": "gpt-entity", "example": {}},
		"translation": {"model": "gpt-translate"}
	}`)); err == nil {
		t.Fatal("expected inline entity_recognition.example to be rejected")
	}

	// Missing example_file must fail Parse.
	if _, err := Parse([]byte(`{
		"entity_recognition": {"model": "gpt-entity", "example_file": "~/missing_er.json"},
		"translation": {"model": "gpt-translate"}
	}`)); err == nil {
		t.Fatal("expected missing entity_recognition.example_file error")
	}

	// Unknown field in external example file must fail Parse.
	badFieldFile := filepath.Join(fakeHome, "bad_field_er.json")
	if err := os.WriteFile(badFieldFile, []byte(`{
		"input": {"transcript": [{"speaker": "A", "text": "x"}]},
		"output": {"entity": []},
		"extra": true
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`{
		"entity_recognition": {"model": "gpt-entity", "example_file": "~/bad_field_er.json"},
		"translation": {"model": "gpt-translate"}
	}`)); err == nil {
		t.Fatal("expected unknown field in example_file to fail Parse")
	}

	// Semantic validation must reject mismatched indices in translation example_file.
	badIndexFile := filepath.Join(fakeHome, "bad_index_tr.json")
	if err := os.WriteFile(badIndexFile, []byte(`{
		"input": {"dialogue": [{"index": 0, "text": "a"}]},
		"output": {"dialogue": [{"index": 1, "text": "b"}]}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Parse([]byte(`{
		"entity_recognition": {"model": "gpt-entity"},
		"translation": {"model": "gpt-translate", "example_file": "~/bad_index_tr.json"}
	}`)); err == nil || !strings.Contains(err.Error(), "translation.example") {
		t.Fatalf("expected invalid translation example error, got: %v", err)
	}
}

func TestProviderEndpointParsing(t *testing.T) {
	defCfg, err := Parse([]byte(`{
		"entity_recognition": {"model": "gpt-entity"},
		"translation": {"model": "gpt-translate"}
	}`))
	if err != nil {
		t.Fatalf("Parse default failed: %v", err)
	}
	if defCfg.Provider.Endpoint != DefaultEndpoint {
		t.Fatalf("default Endpoint = %q, want %q", defCfg.Provider.Endpoint, DefaultEndpoint)
	}

	chatCfg, err := Parse([]byte(`{
		"provider": {"endpoint": "chat_completions"},
		"entity_recognition": {"model": "gpt-entity"},
		"translation": {"model": "gpt-translate"}
	}`))
	if err != nil {
		t.Fatalf("Parse chat_completions failed: %v", err)
	}
	if chatCfg.Provider.Endpoint != "chat_completions" {
		t.Fatalf("Endpoint = %q, want chat_completions", chatCfg.Provider.Endpoint)
	}

	if _, err := Parse([]byte(`{
		"provider": {"endpoint": "invalid"},
		"entity_recognition": {"model": "gpt-entity"},
		"translation": {"model": "gpt-translate"}
	}`)); err == nil {
		t.Fatal("expected invalid provider.endpoint to fail Parse")
	}
}
