# script-translator

[English](README.md) | [繁體中文](README_zh-TW.md)

Multi-language game script translation CLI tool (defaults to Japanese → Traditional Chinese `zh-TW`, with configurable `language` and few-shot `example` prompt templates). It uses an OpenAI-compatible Responses API (with Structured Outputs JSON Schema) for entity recognition and context-aware batched translation, while supporting Unix pipe (`|`) composition, unit-level checkpoint resume, and a strictly read-only persistent entity dictionary (`entity_dict`).

## Installation

### 1. Nix (Flakes)

`flake.nix` downloads prebuilt static binaries directly from GitHub Releases (`x86_64-linux`, `aarch64-linux`, `aarch64-darwin`). Each tagged `vX.Y.Z` release triggers CI to automatically update `flake.nix` on `main` with the latest version and SHA256 hashes.

> Note: Installing via the flake requires at least one published GitHub Release matching the version pinned in `flake.nix`.

```bash
# Run directly without installing
nix run github:ChiaYuChang/script-translator -- --version

# Install into your Nix profile
nix profile install github:ChiaYuChang/script-translator

# Upgrade to the latest release
nix profile upgrade script-translator
```

For NixOS or Home Manager, add it as a flake input:

```nix
{
  inputs.script-translator.url = "github:ChiaYuChang/script-translator";
}
```

#### Home Manager Module

```nix
{inputs, ...}: {
  imports = [
    inputs.script-translator.homeManagerModules.default
  ];

  programs.script-translator = {
    enable = true;
    # Optional: when non-empty, declaratively generates config.json
    # (Linux: ~/.config/script-translator/config.json, macOS: ~/Library/Application Support/script-translator/config.json).
    # Prefer api_key_file (e.g. sops-nix/agenix) or api_key_env over inline api_key because Nix store files are world-readable.
    settings = {
      language = {
        from = "Japanese";
        to = "Traditional Chinese";
        location = "Taiwan (zh-TW)";
      };
      provider = {
        base_url = "https://api.openai.com/v1";
        api_key_file = "/run/secrets/openai_api_key";
        # api_key_env = "OPENAI_API_KEY";
        wait_time = 1;
        retry_time = 5;
      };
      entity_recognition.model = "gpt-4.1-mini";
      translation = {
        model = "gpt-4.1";
        batch_size = 20;
      };
    };
  };
}
```

If you only want the package itself (e.g., for NixOS), use `inputs.script-translator.packages.${pkgs.stdenv.hostPlatform.system}.default` or `inputs.script-translator.overlays.default` (provides `pkgs.script-translator`).

### 2. GitHub Releases

Prebuilt binaries and `SHA256SUMS` are published on [Releases](https://github.com/ChiaYuChang/script-translator/releases):

- `script-translator-linux-amd64`
- `script-translator-linux-arm64`
- `script-translator-darwin-arm64`
- `script-translator-windows-amd64.exe`

```bash
chmod +x script-translator-linux-amd64
./script-translator-linux-amd64 --version
```

### 3. Build from Source

- Go `1.27.3` or later
- OpenAI or compatible Responses API endpoint and API key

```bash
# Build ./bin/script-translator using VERSION from Taskfile.yml
task build

# Or build manually with a version string (defaults to dev if omitted)
go build -ldflags "-X main.version=v0.4.1" -o ./bin/script-translator ./cmd/script-translator

# Or run directly via go run
go run ./cmd/script-translator --help
./bin/script-translator --version
```

## Configuration (`config.json`)

When `-c / --config` is not passed, the CLI reads `~/.config/script-translator/config.json` (resolved via `os.UserConfigDir()`; `~` home expansion is supported across path flags and config paths) with no current-directory fallback. If the file does not exist, it returns an error; run `script-translator default init` to generate a minimal valid config at the default location.

Configuration is loaded and validated against a strict JSON Schema via [`config.Load`](internal/config/config.go) (unknown fields are rejected and defaults are applied automatically):

```json
{
  "cache_dir": "./cache",
  "language": {
    "from": "Japanese",
    "to": "Traditional Chinese",
    "location": "Taiwan (zh-TW)"
  },
  "provider": {
    "base_url": "https://api.openai.com/v1",
    "api_key_env": "OPENAI_API_KEY",
    "wait_time": 0,
    "retry_time": 0
  },
  "entity_recognition": {
    "model": "gpt-4.1-mini",
    "max_attempt": 3
  },
  "translation": {
    "model": "gpt-4.1",
    "batch_size": 20,
    "max_attempt": 3
  },
  "observation": {
    "log": {
      "level": "info",
      "console": {
        "enable": true
      },
      "file": {
        "enable": true,
        "file": "logs/run.jsonl"
      }
    },
    "metric": {
      "enable": true,
      "file": "metrics/usage.jsonl"
    }
  }
}
```

### Field Reference

| Section | Field | Required | Default | Description |
| --- | --- | --- | --- | --- |
| — | `cache_dir` | No | `"./cache"` | Directory where `script run` stores intermediate artifacts keyed by transcript `hashsum` |
| `language` | `from` | No | `"Japanese"` | Source script language injected into prompt templates |
| `language` | `to` | No | `"Traditional Chinese"` | Target localization language injected into prompt templates |
| `language` | `location` | No | `"Taiwan (zh-TW)"` (when `language` omitted) | Optional target region/locale (omitted by default when `language` is explicitly configured) |
| `provider` | `base_url` | No | — | Custom OpenAI-compatible API endpoint (`http` / `https` URL) |
| `provider` | `api_key` | No | — | Explicit inline API key (overrides `api_key_file` and `api_key_env`) |
| `provider` | `api_key_file` | No | — | Path to file containing the API key (supports `~` expansion; overrides `api_key_env`; ideal for NixOS / Home Manager secrets) |
| `provider` | `api_key_env` | No | `"OPENAI_API_KEY"` | Environment variable name holding the API key |
| `provider` | `wait_time` | No | `0` | Delay in seconds between LLM calls / translation batches and before retrying general errors (`0` disables waiting) |
| `provider` | `retry_time` | No | `0` | Base delay in seconds for exponential backoff (`retry_time * 2^0`, `2^1`, ...) on HTTP `429`, `5xx`, transport `unexpected EOF` / `EOF`, and `net.Error` timeouts; on `unexpected EOF`, translation also dynamically shrinks `batch_size` (`max(batch * 9 / 10, 10)` when `batch > 10`) and recalculates `total_batch` |
| `entity_recognition` | `model` | Yes | — | Model name used for entity recognition |
| `entity_recognition` | `max_attempt` | No | `3` | Maximum attempts on entity recognition failure (`>= 1`) |
| `entity_recognition` | `example_file` | No | Built-in Japanese→zh-TW example | Optional path to a custom few-shot example JSON file (`input`, `output`, optional `explanation`; supports `~` expansion) |
| `translation` | `model` | Yes | — | Model name used for dialogue translation |
| `translation` | `batch_size` | No | `20` | Number of dialogue units per translation batch (`>= 1`) |
| `translation` | `max_attempt` | No | `3` | Maximum attempts per translation batch (`>= 1`) |
| `translation` | `example_file` | No | Built-in Japanese→zh-TW example | Optional path to a custom few-shot example JSON file (`input`, `output`, optional `note`; supports `~` expansion) |
| `observation.log` | `level` | No | `"info"` | `debug` / `info` / `warn` / `error` |
| `observation.log.console` | `enable` | No | `true` | Write human-readable logs to `stderr` (never pollutes `stdout` pipes) |
| `observation.log.file` | `enable`, `file` | No | `false` | Append structured JSONL logs (with `time` and UUIDv7 `run_id`) |
| `observation.metric` | `enable`, `file` | No | `false` | Stream JSONL usage metric events in real time |

## CLI Commands & Workflows

Command hierarchy:

```text
script-translator
├── script
│   ├── extract     # Extract dialogue (with index / line_index), retained skeleton, and hashsum to JSON
│   ├── hashsum     # Compute SHA-256 hashsum of normalized transcript (ignores command/comment metadata)
│   ├── recognize   # Extract dialogue and recognize entities to JSON (reads entity_dict read-only)
│   ├── translate   # Call LLM to translate dialogue into intermediate JSON (with hashsum, no assembly)
│   ├── assemble    # Reassemble translated JSON back into target script or extracted skeleton (verifies hashsum)
│   └── run         # Run full pipeline end-to-end with cache_dir/<hashsum>/ reuse and partial resume
├── entity
│   ├── insert      # Merge recognize JSON (pipe/file) or --from/--to pair into entity_dict (alias: import)
│   └── list        # Display entries in entity_dict
├── default
│   ├── all         # List all default paths and parameters
│   ├── which       # Print a single default setting value
│   └── init        # Create a minimal valid config.json at the default config path
├── prompt
│   ├── template    # Print raw .gotmpl template (entity_recognition | translation)
│   ├── example     # Print default few-shot example JSON for editing
│   ├── entity_recognition # Render entity recognition system prompt from config (or --default)
│   └── translation # Render translation system prompt from config (or --default)
└── version         # Print current version (or use --version)
```

> Safety rule: Every `script *` subcommand treats `--dict` (`entity_dict.json`) as strictly **read-only** and never mutates your master dictionary. Review candidates and commit them explicitly with `entity insert`.

### 1. Common Workflows

#### A. End-to-End Execution (`script run` with `hashsum` caching and partial resume)

```bash
export OPENAI_API_KEY="sk-..."

# First run: recognizes + translates, caches under ./cache/<hashsum>/, and writes output
script-translator script run script_v1.txt -d entity_dict.json -o translated_v1.txt

# Script updated with only command/comment metadata changes (same hashsum): cache hit skips LLM calls
script-translator script run script_v2.txt -d entity_dict.json -o translated_v2.txt
```

#### B. Staged Artifacts for Manual Review or Cross-Version Reuse

```bash
# 1. Extract dialogue and script skeleton (extracted.json with hashsum, dialogue, retained)
script-translator script extract -i script_v1.txt -o extracted.json

# 2. Recognize candidate entities (reads entity_dict.json read-only)
script-translator script recognize -i extracted.json -d entity_dict.json -o recognized.json

# 3. Review / merge approved terms into master dictionary (optional)
script-translator entity insert -d entity_dict.json -i recognized.json

# 4. Translate into intermediate translated.json
script-translator script translate -i extracted.json -d entity_dict.json -e recognized.json -o translated.json

# 5. Reassemble into script_v1 or metadata-updated script_v2
script-translator script assemble -i script_v2.txt -t translated.json -d entity_dict.json -o translated_v2.txt
```

#### C. Unix Pipe Composition

Console logs go to `stderr` while `stdout` emits clean JSON, so stages compose directly over pipes:

```bash
script-translator script recognize -i e2e/data/e2e_scene_ver1.txt -d entity_dict.json \
  | script-translator script translate -d entity_dict.json -e - \
  | script-translator script assemble -i e2e/data/e2e_scene_ver1.txt -d entity_dict.json -o output.txt
```

### 2. Subcommand Flag Reference

#### `script-translator script extract [script]`

- `-i, --in`: Source script path (default `"script.txt"`; `"-"` reads `stdin`, or pass as positional arg)
- `-o, --out`: Output `extracted.json` path (default `"-"` writes to `stdout`)

#### `script-translator script hashsum [script]`

- `-i, --in`: Source script or `extracted.json` path (default `"script.txt"`; `"-"` reads `stdin`, or pass as positional arg)
- Does not require `config.json`; outputs SHA-256 of the normalized dialogue `transcript`.

#### `script-translator script recognize`

- `-c, --config`: Config file path (default `~/.config/script-translator/config.json`)
- `-i, --in`: Source script or `extracted.json` path (default `"script.txt"`)
- `-d, --dict`: Existing `entity_dict.json` path (optional, read-only; treated as empty if missing)
- `-o, --out`: Output JSON path (default `"-"`, writes to `stdout`)
- `--save-raw`: Optional directory to save raw HTTP JSON responses from each Responses API call

#### `script-translator script translate`

- `-c, --config`: Config file path (default `~/.config/script-translator/config.json`)
- `-i, --in`: Source script or `extracted.json` path (default `"script.txt"`; inherits `"input"` from `recognize` JSON when `-i` is omitted)
- `-e, --entities`: Path to `recognize` output JSON (or `"-"` for `stdin`); merged in memory with `-d` without modifying `-d`
- `-d, --dict`: Read-only `entity_dict.json` path
- `-o, --out`: Intermediate `translated.json` output path (default `"-"`, writes to `stdout`)
- `--save-raw`: Optional directory to save raw HTTP JSON responses from each Responses API call

#### `script-translator script assemble`

- `-i, --in`: Target script or `extracted.json` skeleton path (default `"script.txt"`)
- `-t, --translated`: Intermediate `translated.json` path (default `"-"` reads `stdin`)
- `-d, --dict`: Read-only `entity_dict.json` path (used for speaker name mapping)
- `-e, --entities`: Optional extra `recognize` JSON path (read-only speaker fallback)
- `-o, --out`: Final localized script path (default `"translated.txt"`; `"-"` writes to `stdout`)
- `--force`: Skip `hashsum` verification between the target script and `translated.json`

#### `script-translator script run [script]`

- `-c, --config`: Config file path
- `-i, --in`: Source script or `extracted.json` path (default `"script.txt"`, or pass as positional arg)
- `-d, --dict`: Read-only `entity_dict.json` path (never modified)
- `-o, --out`: Final localized script path (default `"translated.txt"`; `"-"` writes to `stdout`)
- `--cache-dir`: Override configured `cache_dir` (default `"./cache"`)
- `--no-cache`: Ignore existing `<cache_dir>/<hashsum>/` cache and re-run LLM calls; on normal runs interrupted mid-translation, reads `translated.partial.json` to resume from completed units (works across changed or shrunk `batch_size`)
- `--save-raw`: Optional directory to save raw HTTP JSON responses from each Responses API call

#### `script-translator entity insert` (alias: `import`)

- `-d, --dict`: Target `entity_dict.json` path (**required**)
- `-i, --in`: Input JSON path (`recognize` output or `{"entities": [...]}`, default `"-"` reads `stdin`)
- `--from` / `--to`: Quickly insert a single source/target name pair (mutually exclusive with `-i`)

#### `script-translator entity list`

- `-d, --dict`: Target `entity_dict.json` path (**required**)
- `--json`: Output as JSON

#### `script-translator default all` / `which <key>` / `init`

- `default all`: List all default paths and parameters (`CONFIG`, `INPUT`, `OUTPUT`, `CACHE_DIR`, `APIKEY_ENV`, `BATCH_SIZE`, `MAX_ATTEMPT`, `WAIT_TIME`, `RETRY_TIME`, `LOG_LEVEL`); pass `--json` for JSON output
- `default which <key>`: Print a single default value (case-insensitive, e.g. `CONFIG`, `APIKEY_ENV`, `CACHE_DIR`) for shell scripts
- `default init`: Create a minimal schema-valid `config.json` at `~/.config/script-translator/config.json` (pass `-f / --force` to overwrite an existing file)

#### `script-translator prompt` (`template` / `example` / `entity_recognition` / `translation`)

- `prompt template <entity_recognition|translation>`: Print the raw `.gotmpl` template source
- `prompt example <entity_recognition|translation>`: Export the built-in default example JSON (redirect to a file, customize it, and point `example_file` at it)
- `prompt entity_recognition [-c config.json] [--default]` / `prompt translation [-c config.json] [--default]`: Render the final system prompt sent to the LLM

#### `script-translator version`

- Print CLI version (also `--version`). Defaults to `dev` unless injected via `-ldflags "-X main.version=..."` or `VERSION` in `Taskfile.yml`.

## Observability (Logs & Metrics)

Each invocation generates a unique UUIDv7 `run_id`:

- **Logs** (`observation.log`): Every entry includes `time` and `run_id`; file output appends structured JSONL.
- **Metrics** (`observation.metric`): Streams JSONL events in real time:
  - `entity_usage`: One event per entity recognition attempt
  - `translation_batch_usage`: One event per translation batch attempt
  - `total_usage`: Summary event emitted on completion (including graceful `SIGINT` / `SIGTERM` shutdown) with total requests, errors, retries, and token usage

## Development & Testing

```bash
go test -count=1 ./...
go vet ./...
```

### E2E Tests (`e2e/` & `Taskfile.yml`)

[`e2e/e2e_test.go`](e2e/e2e_test.go) **runs offline by default using recorded responses in `e2e/fixtures/` against a local `httptest.Server`**. Pass `-live` to execute against the real API configured via `.env` (`OPENROUTER_APIKEY`) and `e2e/*/config.json`.

- `e2e/data/`: Sample scripts (`e2e_scene_ver1.txt`, `e2e_scene_ver2.txt`) and initial seed dictionary `seed_entities.json`
- `e2e/fixtures/`: Recorded raw Responses API JSON fixtures (`recognize_001.json`, `translate_001.json`–`translate_004.json`)
- `e2e/recognize/`: Entity recognition config (`config.json`) and stage output directory
- `e2e/translate/`: Translation config (`config.json`) and stage output directory
- `e2e/entity/`: `entity_dict.json` test directory

```bash
# Default offline E2E (fixtures + httptest.Server)
task e2e
# or: go test -v -count=1 ./e2e

# Live API E2E (reads OPENROUTER_APIKEY from .env)
task e2e:live
# or: go test -v -count=1 ./e2e -live

# Call live API and refresh e2e/fixtures/
task e2e:record

# Individual live CLI stage tasks
task e2e:entity:reset
task e2e:hashsum
task e2e:extract
task e2e:recognize
task e2e:translate
task e2e:assemble
task e2e:run
task e2e:entity:insert
task e2e:pipe
```

## License

[MIT](LICENSE)
