# script-translator

[English](README.md) | [繁體中文](README_zh-TW.md)

支援多語系之遊戲腳本翻譯 CLI 工具（預設日文轉繁體中文 `zh-TW`，可透過 `language` 與 few-shot `example` 自訂 prompt 模板）。透過 OpenAI-compatible Responses API（Structured Outputs JSON Schema）進行專有名詞辨識與批次語境翻譯，支援 Unix pipe (`|`) 串接流程、斷點續傳與唯讀式持久化實體對照表（`entity_dict`）。

## 安裝方式

### 1. Nix（Flakes）

`flake.nix` 會直接從 GitHub Releases 下載對應平台的預編譯靜態執行檔（支援 `x86_64-linux`、`aarch64-linux`、`aarch64-darwin`），並在每次推送 `vX.Y.Z` release tag 時由 CI 自動更新 `main` 上的版本與 SHA256。

> 注意：透過 GitHub Release 下載 binary 前，需確保該版本已有對應的 GitHub Release 發布。

```bash
# 不安裝直接執行
nix run github:ChiaYuChang/script-translator -- --version

# 安裝到個人 Nix profile
nix profile install github:ChiaYuChang/script-translator

# 後續升級到最新版本
nix profile upgrade script-translator
```

NixOS / Home Manager 可作為 flake input 引入：

```nix
{
  inputs.script-translator.url = "github:ChiaYuChang/script-translator";
}
```

#### Home Manager 模組

```nix
{inputs, ...}: {
  imports = [
    inputs.script-translator.homeManagerModules.default
  ];

  programs.script-translator = {
    enable = true;
    # 選填：非空時會自動產生 config.json（Linux: ~/.config/script-translator/config.json，macOS: ~/Library/Application Support/script-translator/config.json）
    # 注意 Nix store 為全域可讀，建議使用 api_key_env 而非明文 api_key
    settings = {
      language = {
        from = "Japanese";
        to = "Traditional Chinese";
        location = "Taiwan (zh-TW)";
      };
      provider = {
        base_url = "https://api.openai.com/v1";
        api_key_env = "OPENAI_API_KEY";
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

若僅需套件本體（或搭配 NixOS），亦可直接使用 `inputs.script-translator.packages.${pkgs.stdenv.hostPlatform.system}.default` 或 `inputs.script-translator.overlays.default`（提供 `pkgs.script-translator`）。

### 2. GitHub Releases

可從 [Releases](https://github.com/ChiaYuChang/script-translator/releases) 下載各平台預編譯執行檔與校驗檔 `SHA256SUMS`：

- `script-translator-linux-amd64`
- `script-translator-linux-arm64`
- `script-translator-darwin-arm64`
- `script-translator-windows-amd64.exe`

```bash
chmod +x script-translator-linux-amd64
./script-translator-linux-amd64 --version
```

### 3. 從原始碼建置

- Go `1.27.3` 以上
- OpenAI 或相容 Responses API 的服務與金鑰

```bash
# 建置執行檔 ./bin/script-translator（使用 Taskfile.yml 的 VERSION）
task build

# 或手動編譯並注入版本（未指定時預設為 dev）
go build -ldflags "-X main.version=v0.3.0" -o ./bin/script-translator ./cmd/script-translator

# 或直接以 go run 執行
go run ./cmd/script-translator --help
./bin/script-translator --version
```

## 設定檔（`config.json`）

未傳 `-c / --config` 時，CLI 直接讀取 `~/.config/script-translator/config.json`（由 `os.UserConfigDir()` 展開，亦支援路徑中的 `~` 展開），不進行目前目錄回退；檔案不存在時會直接報錯，可先以 `script-translator default init` 在預設位置產生最簡範本。

CLI 透過 [`config.Load`](internal/config/config.go) 讀取 JSON 設定檔，會以 JSON Schema 驗證結構並自動套用預設值（不允許未知欄位）：

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

### 欄位說明

| 區塊 | 欄位 | 必填 | 預設值 | 說明 |
| --- | --- | --- | --- | --- |
| — | `cache_dir` | 否 | `"./cache"` | `script run` 依 transcript `hashsum` 存放中間檔快取的目錄 |
| `language` | `from` | 否 | `"Japanese"` | 填入 prompt 模板的來源腳本語言 |
| `language` | `to` | 否 | `"Traditional Chinese"` | 填入 prompt 模板的目標翻譯語言 |
| `language` | `location` | 否 | 省略 `language` 時為 `"Taiwan (zh-TW)"` | 選填的目標地區／語系慣用（若手動設定 `language` 且未填 `location`，則不會自動套用） |
| `provider` | `base_url` | 否 | — | 自訂 OpenAI-compatible API endpoint（須為 `http` / `https` URL） |
| `provider` | `api_key` | 否 | — | 直接指定 API key（會覆蓋 `api_key_env`） |
| `provider` | `api_key_env` | 否 | `"OPENAI_API_KEY"` | 讀取 API key 的環境變數名稱 |
| `provider` | `wait_time` | 否 | `0` | 每次 LLM 呼叫／翻譯 batch 之間及一般錯誤重試前的等待秒數（`0` 表示不等待） |
| `provider` | `retry_time` | 否 | `0` | 遇到 HTTP `429`、`5xx`、傳輸層 `unexpected EOF`／`EOF` 或 `net.Error`逾時時的指數退避基準秒數（依序等待 `retry_time * 2^0`, `2^1`, ...；翻譯於 `unexpected EOF` 時還會將 `batch_size` 動態縮小為 `* 9 / 10`，最小為 `1`，並重算 `total_batch`） |
| `entity_recognition` | `model` | 是 | — | 實體辨識使用的模型名稱 |
| `entity_recognition` | `max_attempt` | 否 | `3` | 實體辨識失敗時的最大嘗試次數（`>= 1`） |
| `entity_recognition` | `example` | 否 | 內建日譯繁中範例 | 選填的自訂 few-shot 範例（`input.known_entities`、`input.transcript`、`output.entity`、`explanation`），會格式化填入實體辨識 prompt |
| `translation` | `model` | 是 | — | 對話翻譯使用的模型名稱 |
| `translation` | `batch_size` | 否 | `20` | 每次翻譯請求包含的對話單元數（`>= 1`） |
| `translation` | `max_attempt` | 否 | `3` | 每個翻譯 batch 失敗時的最大嘗試次數（`>= 1`） |
| `translation` | `example` | 否 | 內建日譯繁中範例 | 選填的自訂 few-shot 範例（`input.entries`、`input.history`、`input.dialogue`、`output.dialogue`、`note`），會格式化填入翻譯 prompt |
| `observation.log` | `level` | 否 | `"info"` | `debug` / `info` / `warn` / `error` |
| `observation.log.console` | `enable` | 否 | `true` | 是否輸出人讀 log 到 `stderr`（不會混入 `stdout` pipe） |
| `observation.log.file` | `enable`, `file` | 否 | `false` | 是否附加 JSONL 結構化 log（含 `time` 與 UUIDv7 `run_id`） |
| `observation.metric` | `enable`, `file` | 否 | `false` | 是否即時附加 JSONL metric 事件 |

## CLI 指令與使用方式

指令結構如下：

```text
script-translator
├── script
│   ├── extract     # 抽取腳本對話（含 index / line_index）、retained 骨架與 hashsum 至 JSON
│   ├── hashsum     # 計算腳本 transcript 的 SHA-256 hashsum（忽略 command / comment 等 metadata）
│   ├── recognize   # 抽取腳本對話並辨識實體，輸出 JSON（唯讀讀取 entity_dict）
│   ├── translate   # 呼叫 LLM 翻譯對話，輸出中間譯文 JSON（含 hashsum，不直接組裝）
│   ├── assemble    # 將翻譯中間 JSON 組裝回目標腳本或 extracted 骨架（預設校驗 hashsum）
│   └── run         # 一鍵執行全流程，依 cache_dir/<hashsum>/ 快取自動跳過已翻譯的 LLM 呼叫
├── entity
│   ├── insert      # 從 recognize JSON（pipe / 檔案）或 --from/--to 合併寫入主字典（alias: import）
│   └── list        # 檢視主字典內容
├── default
│   ├── all         # 列出全部預設路徑與參數
│   ├── which       # 查詢單一預設變數值
│   └── init        # 在預設設定位置產生最簡 config.json
└── version         # 顯示目前版本（亦可用 --version）
```

> 安全原則：所有 `script *` 指令對 `--dict`（`entity_dict.json`）一律為**唯讀讀取**，不會自動寫回主字典；請審閱後由 `entity insert` 手動寫入。

### 1. 常見工作流

#### A. 一鍵執行（`script run`，支援 `hashsum` 快取與斷點續傳）

```bash
export OPENAI_API_KEY="sk-..."

# 首次執行：辨識 + 翻譯後自動快取於 ./cache/<hashsum>/ 並輸出譯文
script-translator script run script_v1.txt -d entity_dict.json -o translated_v1.txt

# 僅調整 command / comment 的新版本腳本（相同 hashsum）：命中快取跳過 LLM，直接 assemble
script-translator script run script_v2.txt -d entity_dict.json -o translated_v2.txt
```

#### B. 分階段產出中間檔、人工審閱或跨版本重用

```bash
# 1. 抽取對話與骨架（產生 extracted.json，內含 hashsum、dialogue、retained）
script-translator script extract -i script_v1.txt -o extracted.json

# 2. 辨識候選實體（唯讀參考 entity_dict.json，不修改原檔）
script-translator script recognize -i extracted.json -d entity_dict.json -o recognized.json

# 3. 人工檢查 / 入庫（選用）
script-translator entity insert -d entity_dict.json -i recognized.json

# 4. 產生純翻譯中間檔 translated.json
script-translator script translate -i extracted.json -d entity_dict.json -e recognized.json -o translated.json

# 5. 組裝回 script_v1 或只改了 metadata 的 script_v2
script-translator script assemble -i script_v2.txt -t translated.json -d entity_dict.json -o translated_v2.txt
```

#### C. 直接用 pipe 串接

Console log 輸出在 `stderr`，`stdout` 只保留 JSON，可直接以 `|` 串接：

```bash
script-translator script recognize -i e2e/data/e2e_scene_ver1.txt -d entity_dict.json \
  | script-translator script translate -d entity_dict.json -e - \
  | script-translator script assemble -i e2e/data/e2e_scene_ver1.txt -d entity_dict.json -o output.txt
```

### 2. 子指令 Flag 參考

#### `script-translator script extract [script]`

- `-i, --in`：來源腳本路徑（預設 `"script.txt"`；傳 `"-"` 讀 `stdin`，亦可改用位置參數）
- `-o, --out`：輸出 `extracted.json` 路徑（預設 `"-"` 輸出到 `stdout`）

#### `script-translator script hashsum [script]`

- `-i, --in`：來源腳本或 `extracted.json` 路徑（預設 `"script.txt"`；傳 `"-"` 讀 `stdin`，亦可改用位置參數）
- 不需 `config.json`，依正規化對話 `transcript` 輸出 SHA-256。

#### `script-translator script recognize`

- `-c, --config`：設定檔路徑（預設 `~/.config/script-translator/config.json`）
- `-i, --in`：來源腳本或 `extracted.json` 路徑（預設 `"script.txt"`）
- `-d, --dict`：現有 `entity_dict.json` 路徑（選填・唯讀；檔案不存在時視同空字典）
- `-o, --out`：輸出 JSON 路徑（預設 `"-"`，輸出到 `stdout`）
- `--save-raw`：選填目錄路徑，保留每次 Responses API 原始 HTTP JSON 回應

#### `script-translator script translate`

- `-c, --config`：設定檔路徑（預設 `~/.config/script-translator/config.json`）
- `-i, --in`：來源腳本或 `extracted.json` 路徑（預設 `"script.txt"`；從 `recognize` JSON 讀取且未傳 `-i` 時優先沿用其中的 `"input"`）
- `-e, --entities`：`recognize` 輸出 JSON 檔路徑（或 `"-"` 讀 `stdin`）；可與 `-d` 併用於記憶體內合併（不修改 `-d`）
- `-d, --dict`：唯讀讀取既有 `entity_dict.json`
- `-o, --out`：中間譯文 `translated.json` 輸出路徑（預設 `"-"`，輸出到 `stdout`）
- `--save-raw`：選填目錄路徑，保留每次 Responses API 原始 HTTP JSON 回應

#### `script-translator script assemble`

- `-i, --in`：目標腳本或 `extracted.json` 骨架路徑（預設 `"script.txt"`）
- `-t, --translated`：中間譯文 `translated.json` 路徑（預設 `"-"` 從 `stdin` 讀取）
- `-d, --dict`：唯讀 `entity_dict.json` 路徑（用於說話者名稱映射）
- `-e, --entities`：選填額外 `recognize` JSON 路徑（唯讀補充說話者映射）
- `-o, --out`：最終譯文輸出路徑（預設 `"translated.txt"`；傳 `"-"` 輸出到 `stdout`）
- `--force`：略過目標腳本與 `translated.json` 的 `hashsum` 檢查

#### `script-translator script run [script]`

- `-c, --config`：設定檔路徑
- `-i, --in`：來源腳本或 `extracted.json` 路徑（預設 `"script.txt"`，亦可改用位置參數）
- `-d, --dict`：唯讀 `entity_dict.json` 路徑（絕不寫回）
- `-o, --out`：最終譯文輸出路徑（預設 `"translated.txt"`；傳 `"-"` 輸出到 `stdout`）
- `--cache-dir`：覆寫設定檔 `cache_dir`（設定檔預設 `"./cache"`）
- `--no-cache`：忽略既有 `<cache_dir>/<hashsum>/` 快取，重新呼叫 LLM 並刷新快取；若前次執行於某 batch 中斷，預設會讀取 `translated.partial.json` 從已完成的對話單元接續翻譯（支援調整 `batch_size` 後續跑）
- `--save-raw`：選填目錄路徑，保留每次 Responses API 原始 HTTP JSON 回應

#### `script-translator entity insert`（alias：`import`）

- `-d, --dict`：目標 `entity_dict.json` 路徑（**必填**）
- `-i, --in`：輸入 JSON 檔路徑（支援 `recognize` 輸出或 `{"entities": [...]}`，預設 `"-"` 從 `stdin` 讀取）
- `--from` / `--to`：快速單筆新增一組來源原文與目標譯名對照（不可與 `-i` 同時使用）

#### `script-translator entity list`

- `-d, --dict`：目標 `entity_dict.json` 路徑（**必填**）
- `--json`：改以 JSON 格式輸出

#### `script-translator default all` / `which <key>` / `init`

- `default all`：列出目前所有預設路徑與參數（`CONFIG`、`INPUT`、`OUTPUT`、`CACHE_DIR`、`APIKEY_ENV`、`BATCH_SIZE`、`MAX_ATTEMPT`、`WAIT_TIME`、`RETRY_TIME`、`LOG_LEVEL`）；可加 `--json` 以 JSON 格式輸出
- `default which <key>`：輸出單一指定預設值（不分大小寫，如 `CONFIG`、`APIKEY_ENV`、`CACHE_DIR`），適合搭配 shell script 使用
- `default init`：於預設設定位置（`~/.config/script-translator/config.json`）建立可通過 schema 驗證的最簡 `config.json`（若已存在須加 `-f / --force` 才覆寫）

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

- `e2e/data/`：測試腳本（`e2e_scene_ver1.txt`、`e2e_scene_ver2.txt`）與初始種子字典 `seed_entities.json`
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
task e2e:hashsum
task e2e:extract
task e2e:recognize
task e2e:translate
task e2e:assemble
task e2e:run
task e2e:entity:insert
task e2e:pipe
```

## 授權條款

[MIT](LICENSE)
