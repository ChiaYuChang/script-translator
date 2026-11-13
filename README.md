# internal/llm — Recognition patch

This patch is based on the current Local `go-script-translate` workspace.

- `provider.go`: replaces the unfinished `modes string` field with `model string`, and changes constructor to `NewProvider(client, modelName)`.
- `entity_recognition.go`: one-shot Responses API + strict JSON Schema, matched known entities, conservative candidate merging. Known translations always take precedence. New entity names and aliases must be present in the input.
- `response.go`: validates a completed final Responses output and extracts one JSON text part; reusable later for Translation.
- `entity_recognition_test.go`: matching, collision handling, no mutation, schema checks.

`internal/llm/translation.go` remains unchanged.

To construct the provider from a config elsewhere:

```go
client := openai.NewClient(option.WithBaseURL(cfg.Provider.BaseURL), option.WithAPIKey(apiKey))
p := llm.NewProvider(&client, cfg.Provider.ModelName)
```

The patch does not modify your Local workspace. Once copied, run `gofmt -w internal/llm/*.go` and `go test ./...` with your project's Go toolchain. Strict JSON Schema support varies among OpenAI-compatible providers and model endpoints. Candidates in `entities.json` should be reviewed before database import.
