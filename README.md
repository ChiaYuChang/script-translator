# go-script-translate

日文視覺小說 / 遊戲腳本繁體中文（`zh-TW`）本地化 CLI 工具。使用 OpenAI-compatible Responses API（Structured Outputs JSON Schema）進行專有名詞辨識與批次語境翻譯，並支援透過 Unix pipe (`|`) 串接流程與持久化實體對照表（`entity_dict`）。

## 環境需求與建置

- Go `1.27.3` 以上
- OpenAI 或相容 Responses API 的服務與金鑰

```bash
# 建置執行檔 ./bin/script-translator（建議使用 task build 帶入 Taskfile.yml 的 VERSION）
task build

# 或手動編譯並注入版本（未指定時預設為 dev）
go build -ldflags "-X main.version=v0.1.0" -o ./bin/script-translator ./cmd/script-translator

# 或直接以 go run 執行
go run ./cmd/script-translator --help
./bin/script-translator --version
```

## 設定檔（`config.json`）

未傳 `-c / --config` 時，CLI 會優先讀取目前目錄的 `./config.json`；若不存在則改讀 `~/.config/script-translator/config.json`（由 `os.UserConfigDir()` 解析）。

CLI 透過 [`config.Load`](internal/config/config.go) 讀取 JSON 設定檔，會以 JSON Schema 驗證結構並自動套用預設值（不允許未知欄位）：

```json
{
  "provider": {
    "base_url": "https://api.openai.com/v1",
    "api_key_env": "OPENAI_API_KEY"
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

### 欄位說明

| 區塊 | 欄位 | 必填 | 預設值 | 說明 |
| --- | --- | --- | --- | --- |
| `provider` | `base_url` | 否 | — | 自訂 OpenAI-compatible API endpoint（須為 `http` / `https` URL） |
| `provider` | `api_key` | 否 | — | 直接指定 API key（會覆蓋 `api_key_env`） |
| `provider` | `api_key_env` | 否 | `"OPENAI_API_KEY"` | 讀取 API key 的環境變數名稱 |
| `entity_recognition` | `model` | 是 | — | 實體辨識使用的模型名稱 |
| `entity_recognition` | `max_attempt` | 否 | `3` | 實體辨識失敗時的最大嘗試次數（`>= 1`） |
| `translation` | `model` | 是 | — | 對話翻譯使用的模型名稱 |
| `translation` | `batch_size` | 否 | `20` | 每次翻譯請求包含的對話單元數（`>= 1`） |
| `translation` | `max_attempt` | 否 | `3` | 每個翻譯 batch 失敗時的最大嘗試次數（`>= 1`） |
| `observation.log` | `level` | 否 | `"info"` | `debug` / `info` / `warn` / `error` |
| `observation.log.console` | `enable` | 否 | `true` | 是否輸出人讀 log 到 `stderr`（不會混入 `stdout` pipe） |
| `observation.log.file` | `enable`, `file` | 否 | `false` | 是否附加 JSONL 結構化 log（含 `time` 與 UUIDv7 `run_id`） |
| `observation.metric` | `enable`, `file` | 否 | `false` | 是否即時附加 JSONL metric 事件 |

## CLI 指令與使用方式

指令結構如下：

```text
script-translator
├── pipeline
│   ├── recognize   # 抽取腳本對話並辨識實體，輸出 JSON 到 stdout 或指定檔案
│   └── translate   # 讀取腳本與實體對照（stdin pipe、檔案或主字典），翻譯並組裝腳本
├── entity
│   ├── insert      # 從 recognize JSON（pipe / 檔案）或 --from/--to 合併寫入主字典（alias: import）
│   └── list        # 檢視主字典內容
└── version         # 顯示目前版本（亦可用 --version）
```

### 1. 常見工作流

#### A. 直接用 pipe 串接辨識與翻譯

Console log 輸出在 `stderr`，`stdout` 只保留 JSON / 譯文，可直接以 `|` 串接：

```bash
export OPENAI_API_KEY="sk-..."

./bin/script-translator pipeline recognize -i e2e/data/e2e_scene.txt -d entity_dict.json \
  | ./bin/script-translator pipeline translate -o output.txt
```

`pipeline recognize` 輸出的 JSON 會同時帶 `"input"`（來源腳本路徑）與 `"entities"`，因此後接的 `pipeline translate` 在未傳 `-i` 時會自動沿用該路徑（單獨執行時則預設讀 `script.txt`、寫到 `translated.txt`）。

#### B. 先辨識輸出、人工審閱後再翻譯與入庫

```bash
# 1. 產生候選實體 JSON（不直接修改 entity_dict.json）
./bin/script-translator pipeline recognize -i e2e/data/e2e_scene.txt -d entity_dict.json -o candidates.json

# 2. 人工檢查 / 微調 candidates.json 中的譯名或別名

# 3. 使用審閱後的清單進行翻譯
./bin/script-translator pipeline translate -e candidates.json -o output.txt

# 4. 將確認後的實體與新別名合併寫入 entity_dict.json（全有或全無衝突檢查）
./bin/script-translator entity insert -d entity_dict.json -i candidates.json
```

也可直接用 pipe 將辨識結果寫入字典：

```bash
./bin/script-translator pipeline recognize -i e2e/data/e2e_scene.txt -d entity_dict.json \
  | ./bin/script-translator entity insert -d entity_dict.json
```

#### C. 僅用既有字典進行翻譯

```bash
# 省略 -i / -o 時預設讀取 script.txt 並輸出到 translated.txt
./bin/script-translator pipeline translate -d entity_dict.json
```

### 2. 子指令 Flag 參考

#### `script-translator pipeline recognize`

- `-c, --config`：設定檔路徑（未指定時優先 `./config.json`，否則 `~/.config/script-translator/config.json`）
- `-i, --in`：來源腳本路徑（預設 `"script.txt"`）
- `-d, --dict`：現有 `entity_dict.json` 路徑（選填；檔案不存在時視同空字典）
- `-o, --out`：輸出 JSON 路徑（預設 `"-"`，輸出到 `stdout`）
- `--save-raw`：選填目錄路徑，保留每次 Responses API 原始 HTTP JSON 回應（可作為 fixture）

#### `script-translator pipeline translate`

- `-c, --config`：設定檔路徑（未指定時優先 `./config.json`，否則 `~/.config/script-translator/config.json`）
- `-i, --in`：來源腳本路徑（預設 `"script.txt"`；從 `recognize` JSON 讀取且未傳 `-i` 時優先沿用其中的 `"input"`）
- `-e, --entities`：`recognize` 輸出 JSON 檔路徑（或 `"-"` 讀 `stdin`）；不可與 `-d` 同時指定
- `-d, --dict`：直接使用既有 `entity_dict.json`（不透過 `recognize`）
- `-o, --out`：譯文輸出路徑（預設 `"translated.txt"`；傳 `"-"` 輸出到 `stdout`）
- `--save-raw`：選填目錄路徑，保留每次 Responses API 原始 HTTP JSON 回應（可作為 fixture）

> 讀取優先順序：指定 `--entities` → 指定 `--dict` → 偵測到來自 pipe 的 `stdin`。

#### `script-translator entity insert`（alias：`import`）

- `-d, --dict`：目標 `entity_dict.json` 路徑（**必填**）
- `-i, --in`：輸入 JSON 檔路徑（支援 `recognize` 輸出或 `{"entities": [...]}`，預設 `"-"` 從 `stdin` 讀取）
- `--from` / `--to`：快速單筆新增一組日文與繁中譯名（不可與 `-i` 同時使用）

#### `script-translator entity list`

- `-d, --dict`：目標 `entity_dict.json` 路徑（**必填**）
- `--json`：改以 JSON 格式輸出

#### `script-translator version`

- 顯示版本號（亦可直接使用 `--version`）。預設為 `dev`，透過 `-ldflags "-X main.version=..."` 或 `Taskfile.yml` 的 `VERSION` 注入。

## Observability（Logs 與 Metrics）

每次執行會產生唯一的 UUIDv7 `run_id`：

- **Logs**（`observation.log`）：每筆包含 `time` 與 `run_id`，`file` 目標以 JSONL 格式附加。
- **Metrics**（`observation.metric`）：以 JSONL 格式即時附加寫入：
  - `entity_usage`：每次實體辨識 attempt 寫一筆
  - `translation_batch_usage`：每個翻譯 batch 每次 attempt 寫一筆
  - `total_usage`：執行結束（包含 `SIGINT` / `SIGTERM` 正常中止）時補寫一筆該 `run_id` 的總請求數、錯誤數、重試數與 token 消耗彙總

## 開發與測試

```bash
go test -count=1 ./...
go vet ./...
```

### E2E 測試（`e2e/` 與 `Taskfile.yml`）

[`e2e/e2e_test.go`](e2e/e2e_test.go) **預設使用 `e2e/fixtures/` 內錄製的 raw response 搭配本機 `httptest.Server` 執行**（離線快速驗證）；加上 `-live` flag 才會依 `.env` 的 `OPENROUTER_APIKEY` 與 `e2e/*/config.json` 呼叫真實 API。

- `e2e/data/`：測試腳本（含 `e2e_scene.txt`、`input.txt`、`input_variant.txt`）與初始種子字典 `seed_entities.json`
- `e2e/fixtures/`：保留下來的 raw Responses API JSON fixtures（`recognize_001.json`、`translate_001.json`～`translate_004.json`）
- `e2e/recognize/`：實體辨識設定 `config.json` 與單步測試輸出目錄
- `e2e/translate/`：翻譯設定 `config.json` 與單步測試輸出目錄
- `e2e/entity/`：`entity_dict.json` 管理測試目錄

```bash
# 預設離線 E2E（fixture + httptest.Server）
task e2e
# 或 go test -v -count=1 ./e2e

# 呼叫真實 API 跑 E2E（讀取 .env 中的 OPENROUTER_APIKEY）
task e2e:live
# 或 go test -v -count=1 ./e2e -live

# 呼叫真實 API 並更新 e2e/fixtures/
task e2e:record

# 個別階段 CLI 測試（針對真實 API 並保留 raw response）
task e2e:entity:reset
task e2e:recognize
task e2e:translate
task e2e:entity:insert
task e2e:pipe
```
