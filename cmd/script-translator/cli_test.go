package main

import (
	"bytes"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-script-translate/internal/config"
	"go-script-translate/internal/entitystore"
	"go-script-translate/internal/model"
)

func writeTestScript(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "script.txt")
	content := strings.Join([]string{
		"// intro comment",
		"<BG>bg_morning",
		"<NAME_PLATE>新堂ミナト",
		"「秋葉原へ行こう」",
		"（財布、持ってるよな……）",
		"<PAUSE>",
		"<NAME_PLATE>",
		"夕方の教室。",
		"<PAUSE>",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeTestConfig(t *testing.T, dir, baseURL, logPath, metricPath string) string {
	t.Helper()
	path := filepath.Join(dir, "config.json")
	cfg := map[string]any{
		"provider": map[string]any{
			"base_url": baseURL,
			"api_key":  "test-key",
		},
		"entity_recognition": map[string]any{
			"model":       "gpt-entity",
			"max_attempt": 1,
		},
		"translation": map[string]any{
			"model":       "gpt-translate",
			"batch_size":  10,
			"max_attempt": 1,
		},
		"observation": map[string]any{
			"log": map[string]any{
				"level":   "info",
				"console": map[string]any{"enable": true},
				"file":    map[string]any{"enable": true, "file": logPath},
			},
			"metric": map[string]any{
				"enable": true,
				"file":   metricPath,
			},
		},
	}
	b, err := marshalIndentedLine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeMockResponsesServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request: %v", err)
		}
		var req map[string]any
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal request: %v", err)
		}
		modelName, _ := req["model"].(string)

		var outputText string
		switch modelName {
		case "gpt-entity":
			b, err := json.Marshal(map[string]any{
				"entity": []map[string]any{
					{
						"canonical": map[string]any{"from": "秋葉原", "to": "秋葉原"},
						"aliases":   []map[string]any{},
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			outputText = string(b)
		case "gpt-translate":
			prompt, _ := req["input"].(string)
			_, inputJSON, ok := strings.Cut(prompt, "\nInput:\n")
			if !ok {
				t.Fatalf("translation prompt missing Input section:\n%s", prompt)
			}
			var units []model.TranslateUnit
			if err := json.Unmarshal([]byte(inputJSON), &units); err != nil {
				t.Fatalf("unmarshal translation input units: %v\ninput:\n%s", err, inputJSON)
			}
			if len(units) != 3 {
				t.Fatalf("expected 3 units, got %d", len(units))
			}
			b, err := json.Marshal(map[string]any{
				"dialogue": []map[string]any{
					{"index": units[0].Index, "text": "「去秋葉原吧」"},
					{"index": units[1].Index, "text": "（我應該有帶錢包吧……）"},
					{"index": units[2].Index, "text": "傍晚的教室。"},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			outputText = string(b)
		default:
			t.Fatalf("unexpected model name %q", modelName)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("\n         \n         \n"))
		if err := json.MarshalWrite(w, map[string]any{
			"id":     "resp_cli_test",
			"object": "response",
			"status": "completed",
			"usage": map[string]any{
				"input_tokens":  40,
				"output_tokens": 15,
				"total_tokens":  55,
			},
			"output": []map[string]any{
				{
					"id":     "msg_cli_test",
					"type":   "message",
					"status": "completed",
					"role":   "assistant",
					"content": []map[string]any{
						{"type": "output_text", "text": outputText},
					},
				},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}))
}

func TestScriptStagedExtractRecognizeTranslateAssembleAndEntityInsert(t *testing.T) {
	dir := t.TempDir()
	srv := writeMockResponsesServer(t)
	defer srv.Close()

	scriptPath := writeTestScript(t, dir)
	dictPath := filepath.Join(dir, "entity_dict.json")
	if err := entitystore.Save(dictPath, model.EntityMapping{
		Entities: []model.Entity{{
			ID:        1,
			Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
			Aliases:   []model.NamePair{},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	origDictBytes, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatal(err)
	}

	logPath := filepath.Join(dir, "logs.jsonl")
	metricPath := filepath.Join(dir, "metrics.jsonl")
	cfgPath := writeTestConfig(t, dir, srv.URL+"/v1", logPath, metricPath)

	// Step 1: script extract -> extracted.json
	extractedPath := filepath.Join(dir, "extracted.json")
	extCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	extCmd.SetArgs([]string{"script", "extract", "-i", scriptPath, "-o", extractedPath})
	if err := extCmd.Execute(); err != nil {
		t.Fatalf("script extract failed: %v", err)
	}

	// Step 2: script recognize -> stdout (stderr keeps logs isolated) + save raw response.
	rawDir := filepath.Join(dir, "raw")
	var recStdout, recStderr bytes.Buffer
	recCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &recStdout,
		stderr:     &recStderr,
		stdinIsTTY: func() bool { return true },
	})
	recCmd.SetArgs([]string{
		"script", "recognize",
		"-c", cfgPath,
		"-i", extractedPath,
		"-d", dictPath,
		"--save-raw", rawDir,
	})
	if err := recCmd.Execute(); err != nil {
		t.Fatalf("script recognize failed: %v\nstderr:\n%s", err, recStderr.String())
	}
	rawBytes, err := os.ReadFile(filepath.Join(rawDir, "recognize_001.json"))
	if err != nil {
		t.Fatalf("expected raw response fixture recognize_001.json: %v", err)
	}
	if strings.HasPrefix(string(rawBytes), "\n") || strings.HasPrefix(string(rawBytes), " ") {
		t.Fatalf("expected leading keep-alive whitespace trimmed in raw fixture, got: %q", string(rawBytes[:min(40, len(rawBytes))]))
	}

	payload, err := decodeRecognizePayload(recStdout.Bytes())
	if err != nil {
		t.Fatalf("decode recognize stdout: %v\nraw:\n%s", err, recStdout.String())
	}
	if payload.Input != scriptPath || payload.Hashsum == "" || len(payload.Entities) != 2 {
		t.Fatalf("unexpected recognize payload: %#v", payload)
	}

	// Step 3: pipe recognize output -> script translate -> translated.json (uses input from extracted.json or recognize input)
	translatedJSONPath := filepath.Join(dir, "translated.json")
	var trStdout, trStderr bytes.Buffer
	trCmd := newRootCmd(cliDeps{
		stdin:      bytes.NewReader(recStdout.Bytes()),
		stdout:     &trStdout,
		stderr:     &trStderr,
		stdinIsTTY: func() bool { return false },
	})
	trCmd.SetArgs([]string{
		"script", "translate",
		"-c", cfgPath,
		"-i", extractedPath,
		"-e", "-",
		"-o", translatedJSONPath,
	})
	if err := trCmd.Execute(); err != nil {
		t.Fatalf("script translate failed: %v\nstderr:\n%s", err, trStderr.String())
	}

	// Verify entity_dict.json remained untouched during script recognize/translate.
	afterDictBytes, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(origDictBytes, afterDictBytes) {
		t.Fatalf("script stages unexpectedly modified entity_dict.json:\nbefore:\n%s\nafter:\n%s", origDictBytes, afterDictBytes)
	}

	// Step 4: script merge translated.json onto BOTH original script and a metadata-variant script -> translated.txt
	outScriptPath := filepath.Join(dir, "translated.txt")
	mergeCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	mergeCmd.SetArgs([]string{
		"script", "assemble",
		"-i", extractedPath,
		"-t", translatedJSONPath,
		"-d", dictPath,
		"-o", outScriptPath,
	})
	if err := mergeCmd.Execute(); err != nil {
		t.Fatalf("script merge failed: %v", err)
	}

	translatedBytes, err := os.ReadFile(outScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	translatedText := string(translatedBytes)
	if !strings.Contains(translatedText, "<NAME_PLATE>新堂湊") ||
		!strings.Contains(translatedText, "「去秋葉原吧」") ||
		!strings.Contains(translatedText, "（我應該有帶錢包吧……）") ||
		!strings.Contains(translatedText, "傍晚的教室。") {
		t.Fatalf("unexpected merged script:\n%s", translatedText)
	}

	// Verify merging the same translated.json onto a metadata-edited script succeeds and keeps new metadata.
	variantPath := filepath.Join(dir, "variant.txt")
	variantRaw := "// updated header\n<BGM_PLAY>bgm_new,500\n<BG>bg_night\n\n<NAME_PLATE>新堂ミナト\n「秋葉原へ行こう」\n<VOICE>v001\n<NAME_PLATE>新堂ミナト\n（財布、持ってるよな……）\n<NAME_PLATE>\n夕方の教室。\n"
	if err := os.WriteFile(variantPath, []byte(variantRaw), 0o600); err != nil {
		t.Fatal(err)
	}
	var varMergedOut bytes.Buffer
	varMergeCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &varMergedOut,
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	varMergeCmd.SetArgs([]string{
		"script", "assemble",
		"-i", variantPath,
		"-t", translatedJSONPath,
		"-d", dictPath,
		"-o", "-",
	})
	if err := varMergeCmd.Execute(); err != nil {
		t.Fatalf("variant script merge failed: %v", err)
	}
	for _, want := range []string{"// updated header", "<BGM_PLAY>bgm_new,500", "<VOICE>v001", "<NAME_PLATE>新堂湊", "「去秋葉原吧」", "（我應該有帶錢包吧……）", "傍晚的教室。"} {
		if !strings.Contains(varMergedOut.String(), want) {
			t.Fatalf("variant merged output missing %q:\n%s", want, varMergedOut.String())
		}
	}

	// Mismatched hashsum is rejected unless --force is supplied.
	differentPath := filepath.Join(dir, "different.txt")
	if err := os.WriteFile(differentPath, []byte("<NAME_PLATE>新堂ミナト\n「別セリフ」\n（別のモノローグ）\n<NAME_PLATE>\n夕方の教室。\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	mismatchCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	mismatchCmd.SetArgs([]string{"script", "assemble", "-i", differentPath, "-t", translatedJSONPath, "-o", "-"})
	if err := mismatchCmd.Execute(); err == nil {
		t.Fatal("expected hashsum mismatch error on script assemble without --force")
	}
	forceCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	forceCmd.SetArgs([]string{"script", "assemble", "-i", differentPath, "-t", translatedJSONPath, "-o", "-", "--force"})
	if err := forceCmd.Execute(); err != nil {
		t.Fatalf("expected --force assemble to succeed: %v", err)
	}

	// Step 5: pipe recognize output -> entity insert, then list.
	var insStdout, insStderr bytes.Buffer
	insCmd := newRootCmd(cliDeps{
		stdin:      bytes.NewReader(recStdout.Bytes()),
		stdout:     &insStdout,
		stderr:     &insStderr,
		stdinIsTTY: func() bool { return false },
	})
	insCmd.SetArgs([]string{
		"entity", "insert",
		"-d", dictPath,
	})
	if err := insCmd.Execute(); err != nil {
		t.Fatalf("entity insert from pipe failed: %v", err)
	}

	var listStdout bytes.Buffer
	listCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &listStdout,
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	listCmd.SetArgs([]string{
		"entity", "list",
		"-d", dictPath,
		"--json",
	})
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("entity list failed: %v", err)
	}

	var listed model.EntityMapping
	if err := json.Unmarshal(listStdout.Bytes(), &listed); err != nil {
		t.Fatalf("unmarshal list --json: %v\nraw:\n%s", err, listStdout.String())
	}
	if len(listed.Entities) != 2 || listed.Entities[1].ID != 2 || listed.Entities[1].Canonical.From != "秋葉原" {
		t.Fatalf("unexpected entities after insert: %#v", listed.Entities)
	}

	// Verify metric file contains both runs' usage events and closing total_usage events.
	metricBytes, err := os.ReadFile(metricPath)
	if err != nil {
		t.Fatal(err)
	}
	metricLines := strings.Split(strings.TrimSpace(string(metricBytes)), "\n")
	if len(metricLines) != 4 {
		t.Fatalf("expected 4 metric lines (entity_usage + total_usage + translation_batch_usage + total_usage), got %d:\n%s",
			len(metricLines), string(metricBytes))
	}
}

func TestScriptTranslateStandaloneWithDictAndPipeToAssemble(t *testing.T) {
	dir := t.TempDir()
	srv := writeMockResponsesServer(t)
	defer srv.Close()

	scriptPath := writeTestScript(t, dir)
	dictPath := filepath.Join(dir, "entity_dict.json")
	if err := entitystore.Save(dictPath, model.EntityMapping{
		Entities: []model.Entity{{
			ID:        1,
			Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
			Aliases:   []model.NamePair{},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	cfgPath := writeTestConfig(t, dir, srv.URL+"/v1", filepath.Join(dir, "log.jsonl"), filepath.Join(dir, "metric.jsonl"))

	var trOut, trErr bytes.Buffer
	trCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{}, // simulates empty /dev/null non-TTY stdin
		stdout:     &trOut,
		stderr:     &trErr,
		stdinIsTTY: func() bool { return false },
	})
	trCmd.SetArgs([]string{
		"script", "translate",
		"-c", cfgPath,
		"-i", scriptPath,
		"-d", dictPath,
		"-o", "-",
	})
	if err := trCmd.Execute(); err != nil {
		t.Fatalf("standalone translate with --dict failed: %v", err)
	}

	// Pipe translated.json into `script merge`.
	var mergeOut bytes.Buffer
	mergeCmd := newRootCmd(cliDeps{
		stdin:      bytes.NewReader(trOut.Bytes()),
		stdout:     &mergeOut,
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return false },
	})
	mergeCmd.SetArgs([]string{
		"script", "assemble",
		"-i", scriptPath,
		"-d", dictPath,
		"-o", "-",
	})
	if err := mergeCmd.Execute(); err != nil {
		t.Fatalf("piped script merge failed: %v", err)
	}
	if !strings.Contains(mergeOut.String(), "<NAME_PLATE>新堂湊") {
		t.Fatalf("expected merged output on stdout, got:\n%s", mergeOut.String())
	}
}

func TestScriptRunCacheHitAndNoCache(t *testing.T) {
	dir := t.TempDir()
	srv := writeMockResponsesServer(t)

	script1Path := writeTestScript(t, dir)
	script2Path := filepath.Join(dir, "script_v2.txt")
	script2Raw := "// updated meta\n<BGM_PLAY>bgm_new,300\n<NAME_PLATE>新堂ミナト\n「秋葉原へ行こう」\n<VOICE>v1\n<NAME_PLATE>新堂ミナト\n（財布、持ってるよな……）\n<NAME_PLATE>\n夕方の教室。\n"
	if err := os.WriteFile(script2Path, []byte(script2Raw), 0o600); err != nil {
		t.Fatal(err)
	}

	dictPath := filepath.Join(dir, "entity_dict.json")
	if err := entitystore.Save(dictPath, model.EntityMapping{
		Entities: []model.Entity{{
			ID:        1,
			Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
			Aliases:   []model.NamePair{},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	origDictBytes, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(dir, "custom-cache")
	cfgPath := filepath.Join(dir, "config.json")
	cfgRaw := mustMarshalJSONForTest(t, map[string]any{
		"cache_dir": cacheDir,
		"provider": map[string]any{
			"base_url": srv.URL + "/v1",
			"api_key":  "test-key",
		},
		"entity_recognition": map[string]any{"model": "gpt-entity", "max_attempt": 1},
		"translation":        map[string]any{"model": "gpt-translate", "batch_size": 10, "max_attempt": 1},
		"observation": map[string]any{
			"log":    map[string]any{"level": "info", "console": map[string]any{"enable": false}},
			"metric": map[string]any{"enable": true, "file": filepath.Join(dir, "run_metrics.jsonl")},
		},
	})
	if err := os.WriteFile(cfgPath, cfgRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	// First run: executes LLM recognize + translate and writes cache/<hashsum>/...
	out1Path := filepath.Join(dir, "out1.txt")
	run1 := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	run1.SetArgs([]string{"script", "run", script1Path, "-c", cfgPath, "-d", dictPath, "-o", out1Path})
	if err := run1.Execute(); err != nil {
		t.Fatalf("first script run failed: %v", err)
	}

	// Confirm dict.json was NEVER modified by `script run`.
	afterDictBytes, err := os.ReadFile(dictPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(origDictBytes, afterDictBytes) {
		t.Fatal("script run modified read-only dict.json")
	}

	// Close mock HTTP server completely: second run on script_v2 MUST succeed purely from cache!
	srv.Close()

	out2Path := filepath.Join(dir, "out2.txt")
	run2 := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	run2.SetArgs([]string{"script", "run", script2Path, "-c", cfgPath, "-d", dictPath, "-o", out2Path})
	if err := run2.Execute(); err != nil {
		t.Fatalf("cached script run failed after server shutdown: %v", err)
	}

	out2Bytes, err := os.ReadFile(out2Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"<BGM_PLAY>bgm_new,300", "<VOICE>v1", "<NAME_PLATE>新堂湊", "「去秋葉原吧」", "（我應該有帶錢包吧……）", "傍晚的教室。"} {
		if !strings.Contains(string(out2Bytes), want) {
			t.Fatalf("cached run output missing %q:\n%s", want, string(out2Bytes))
		}
	}

	// With --no-cache and server still closed, it must attempt fresh LLM call and fail cleanly.
	runNoCache := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &bytes.Buffer{},
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	runNoCache.SetArgs([]string{"script", "run", script2Path, "-c", cfgPath, "-d", dictPath, "-o", "-", "--no-cache"})
	if err := runNoCache.Execute(); err == nil {
		t.Fatal("expected --no-cache run to fail when API server is closed")
	}
}

func mustMarshalJSONForTest(t *testing.T, v any) []byte {
	t.Helper()
	b, err := marshalIndentedLine(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestEntityInsertInlineAndListText(t *testing.T) {
	dir := t.TempDir()
	dictPath := filepath.Join(dir, "entity_dict.json")

	var insOut bytes.Buffer
	insCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &insOut,
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	insCmd.SetArgs([]string{
		"entity", "insert",
		"-d", dictPath,
		"--from", "朝倉ユイ",
		"--to", "朝倉由依",
	})
	if err := insCmd.Execute(); err != nil {
		t.Fatalf("inline entity insert failed: %v", err)
	}

	var listOut bytes.Buffer
	listCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &listOut,
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	listCmd.SetArgs([]string{
		"entity", "list",
		"-d", dictPath,
	})
	if err := listCmd.Execute(); err != nil {
		t.Fatalf("entity list failed: %v", err)
	}
	if !strings.Contains(listOut.String(), "#1") || !strings.Contains(listOut.String(), "- 朝倉ユイ: 朝倉由依") {
		t.Fatalf("unexpected entity list text output:\n%s", listOut.String())
	}
}

func TestVersionAndDefaultPaths(t *testing.T) {
	// Verify default version output via both `version` command and `--version`.
	var verOut bytes.Buffer
	verCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &verOut,
		stderr:     &bytes.Buffer{},
		stdinIsTTY: func() bool { return true },
	})
	verCmd.SetArgs([]string{"version"})
	if err := verCmd.Execute(); err != nil {
		t.Fatalf("version subcommand failed: %v", err)
	}
	if !strings.Contains(verOut.String(), "script-translator version "+version) {
		t.Fatalf("unexpected version output: %q", verOut.String())
	}

	// Verify fallback to <userConfigDir>/script-translator/config.json and default
	// script.txt -> translated.txt when no -c/-i/-o flags are supplied.
	workDir := t.TempDir()
	userCfgRoot := t.TempDir()
	appCfgDir := filepath.Join(userCfgRoot, appName)
	if err := os.MkdirAll(appCfgDir, 0o750); err != nil {
		t.Fatal(err)
	}

	srv := writeMockResponsesServer(t)
	defer srv.Close()

	writeTestScript(t, workDir) // creates script.txt in workDir
	tmpCfg := writeTestConfig(t, t.TempDir(), srv.URL+"/v1", filepath.Join(workDir, "log.jsonl"), filepath.Join(workDir, "metric.jsonl"))
	cfgBytes, err := os.ReadFile(tmpCfg)
	if err != nil {
		t.Fatal(err)
	}
	userCfgPath := filepath.Join(appCfgDir, "config.json")
	if err := os.WriteFile(userCfgPath, cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(workDir)

	var pipeOut, errBuf bytes.Buffer
	cmd := newRootCmd(cliDeps{
		stdin:         &bytes.Buffer{},
		stdout:        &pipeOut,
		stderr:        &errBuf,
		stdinIsTTY:    func() bool { return true },
		userConfigDir: func() (string, error) { return userCfgRoot, nil },
	})
	cmd.SetArgs([]string{"script", "run"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("default script run failed: %v\nstderr:\n%s", err, errBuf.String())
	}

	translatedBytes, err := os.ReadFile(filepath.Join(workDir, "translated.txt"))
	if err != nil {
		t.Fatalf("expected default translated.txt output: %v", err)
	}
	if !strings.Contains(string(translatedBytes), "去秋葉原吧") {
		t.Fatalf("unexpected default translated.txt:\n%s", string(translatedBytes))
	}

	// Verify ./config.json in cwd does NOT override user config path; resolution is strict without fallback.
	resolved, err := resolveConfigPath("", cliDeps{userConfigDir: func() (string, error) { return userCfgRoot, nil }})
	if err != nil || resolved != userCfgPath {
		t.Fatalf("expected user config path %q, got %q (err=%v)", userCfgPath, resolved, err)
	}
	if err := os.WriteFile(filepath.Join(workDir, "config.json"), cfgBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err = resolveConfigPath("", cliDeps{userConfigDir: func() (string, error) { return userCfgRoot, nil }})
	if err != nil || resolved != userCfgPath {
		t.Fatalf("expected strict user config path %q even with local config.json, got %q (err=%v)", userCfgPath, resolved, err)
	}
	// Missing explicit or default config fails directly without falling back to ./config.json.
	if _, _, err := initConfigAndObserver(filepath.Join(workDir, "missing.json"), cliDeps{}); err == nil {
		t.Fatal("expected error for missing explicit config path")
	}
	missingUserCfgRoot := filepath.Join(workDir, "empty-xdg-config")
	if _, _, err := initConfigAndObserver("", cliDeps{userConfigDir: func() (string, error) { return missingUserCfgRoot, nil }}); err == nil {
		t.Fatal("expected error when default user config file does not exist (no fallback to ./config.json)")
	}
}

func TestCLIScriptHashsum(t *testing.T) {
	workDir := t.TempDir()
	basePath := writeTestScript(t, workDir) // creates script.txt

	variantContent := strings.Join([]string{
		"// different author comment",
		"// extra scene note",
		"<BGM_PLAY>bgm_morning,500",
		"<BG>bg_night",
		"",
		"<NAME_PLATE>新堂ミナト",
		"「秋葉原へ行こう」",
		"<VOICE>minato_01",
		"<NAME_PLATE>新堂ミナト",
		"（財布、",
		"持ってるよな……）",
		"",
		"<NAME_PLATE>",
		"夕方の教室。",
	}, "\n") + "\n"
	variantPath := filepath.Join(workDir, "variant.txt")
	if err := os.WriteFile(variantPath, []byte(variantContent), 0o600); err != nil {
		t.Fatal(err)
	}

	differentContent := strings.Join([]string{
		"// intro comment",
		"<BG>bg_morning",
		"<NAME_PLATE>新堂ミナト",
		"「渋谷へ行こう」",
		"<PAUSE>",
	}, "\n") + "\n"
	differentPath := filepath.Join(workDir, "different.txt")
	if err := os.WriteFile(differentPath, []byte(differentContent), 0o600); err != nil {
		t.Fatal(err)
	}

	runHashsum := func(stdinContent string, args ...string) (string, error) {
		var out bytes.Buffer
		cmd := newRootCmd(cliDeps{
			stdin:      strings.NewReader(stdinContent),
			stdout:     &out,
			stderr:     &bytes.Buffer{},
			stdinIsTTY: func() bool { return true },
		})
		cmd.SetArgs(append([]string{"script", "hashsum"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	t.Chdir(workDir)

	// Default -i script.txt when no flags or positional args are passed.
	defaultOut, err := runHashsum("")
	if err != nil {
		t.Fatalf("default pipeline hashsum failed: %v", err)
	}
	hashDefault := strings.TrimSpace(defaultOut)
	if len(hashDefault) != 64 {
		t.Fatalf("expected 64-char sha256 hex, got %q", defaultOut)
	}

	// Equivalent dialogue with different comments/commands via -i flag.
	variantOut, err := runHashsum("", "-i", variantPath)
	if err != nil {
		t.Fatalf("variant pipeline hashsum failed: %v", err)
	}
	if strings.TrimSpace(variantOut) != hashDefault {
		t.Fatalf("expected equal hashsum for variant metadata, got %q vs %q", variantOut, defaultOut)
	}

	// Positional argument and stdin ('-') both work and match.
	posOut, err := runHashsum("", variantPath)
	if err != nil {
		t.Fatalf("positional pipeline hashsum failed: %v", err)
	}
	if strings.TrimSpace(posOut) != hashDefault {
		t.Fatalf("positional hashsum mismatch: got %q, want %q", posOut, hashDefault)
	}

	stdinOut, err := runHashsum(variantContent, "-i", "-")
	if err != nil {
		t.Fatalf("stdin pipeline hashsum failed: %v", err)
	}
	if strings.TrimSpace(stdinOut) != hashDefault {
		t.Fatalf("stdin hashsum mismatch: got %q, want %q", stdinOut, hashDefault)
	}

	// Changed dialogue produces a different hashsum.
	diffOut, err := runHashsum("", "-i", differentPath)
	if err != nil {
		t.Fatalf("different pipeline hashsum failed: %v", err)
	}
	if strings.TrimSpace(diffOut) == hashDefault {
		t.Fatalf("expected different hashsum when dialogue text differs: %q", diffOut)
	}

	// Combining positional path and --in is rejected, and explicit -i still works cleanly.
	if _, err := runHashsum("", "-i", basePath, variantPath); err == nil {
		t.Fatal("expected error when combining --in and positional path")
	}
	if _, err := runHashsum("", "-i", filepath.Join(workDir, "missing.txt")); err == nil {
		t.Fatal("expected error for missing input file")
	}
}

func TestCLI_DefaultAllAndWhich(t *testing.T) {
	workDir := t.TempDir()
	fakeUserConfig := filepath.Join(workDir, "xdg-config")
	wantUserConfigPath := filepath.Join(fakeUserConfig, "script-translator", "config.json")

	t.Chdir(workDir)

	runDefault := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := newRootCmd(cliDeps{
			stdin:         strings.NewReader(""),
			stdout:        &out,
			stderr:        &bytes.Buffer{},
			stdinIsTTY:    func() bool { return true },
			userConfigDir: func() (string, error) { return fakeUserConfig, nil },
		})
		cmd.SetArgs(append([]string{"default"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	allOut, err := runDefault("all")
	if err != nil {
		t.Fatalf("default all failed: %v", err)
	}
	for _, want := range []string{
		"CONFIG",
		wantUserConfigPath,
		"INPUT",
		"script.txt",
		"OUTPUT",
		"translated.txt",
		"CACHE_DIR",
		"./cache",
		"APIKEY_ENV",
		"OPENAI_API_KEY",
		"BATCH_SIZE",
		"20",
		"MAX_ATTEMPT",
		"3",
		"LOG_LEVEL",
		"info",
	} {
		if !strings.Contains(allOut, want) {
			t.Fatalf("expected %q in default all output, got:\n%s", want, allOut)
		}
	}

	jsonOut, err := runDefault("all", "--json")
	if err != nil {
		t.Fatalf("default all --json failed: %v", err)
	}
	var report defaultReport
	if err := json.Unmarshal([]byte(jsonOut), &report); err != nil {
		t.Fatalf("unmarshal default all --json: %v", err)
	}
	if report.Config != wantUserConfigPath || report.APIKeyEnv != "OPENAI_API_KEY" || report.CacheDir != "./cache" {
		t.Fatalf("unexpected defaultReport: %+v", report)
	}

	// `which` supports canonical keys, lowercase/hyphen normalization, and API_KEY_ENV alias.
	for _, tc := range []struct {
		arg  string
		want string
	}{
		{arg: "CONFIG", want: wantUserConfigPath},
		{arg: "config", want: wantUserConfigPath},
		{arg: "APIKEY_ENV", want: "OPENAI_API_KEY"},
		{arg: "api_key_env", want: "OPENAI_API_KEY"},
		{arg: "cache-dir", want: "./cache"},
		{arg: "BATCH_SIZE", want: "20"},
	} {
		got, err := runDefault("which", tc.arg)
		if err != nil {
			t.Fatalf("default which %q failed: %v", tc.arg, err)
		}
		if strings.TrimSpace(got) != tc.want {
			t.Fatalf("default which %q = %q, want %q", tc.arg, strings.TrimSpace(got), tc.want)
		}
	}

	// Even when ./config.json exists in cwd, CONFIG resolves strictly to user config path.
	if err := os.WriteFile(filepath.Join(workDir, "config.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	localCfgOut, err := runDefault("which", "CONFIG")
	if err != nil {
		t.Fatalf("default which CONFIG with local config.json failed: %v", err)
	}
	if strings.TrimSpace(localCfgOut) != wantUserConfigPath {
		t.Fatalf("expected CONFIG=%q without local fallback, got %q", wantUserConfigPath, localCfgOut)
	}

	// Unknown key returns clear error listing valid keys.
	if _, err := runDefault("which", "NO_SUCH_KEY"); err == nil {
		t.Fatal("expected error for unknown default key")
	}
}

func TestCLIScriptExtractFallbackEmitsStderrWarning(t *testing.T) {
	dir := t.TempDir()
	cutScriptPath := filepath.Join(dir, "cut.txt")
	if err := os.WriteFile(cutScriptPath, []byte("<NAME_PLATE>俺\n（コマンドで切れた\n<VOICE>v1\n<NAME_PLATE>俺\n残り）\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdoutBuf, stderrBuf bytes.Buffer
	cmd := newRootCmd(cliDeps{
		stdin:      strings.NewReader(""),
		stdout:     &stdoutBuf,
		stderr:     &stderrBuf,
		stdinIsTTY: func() bool { return true },
	})
	cmd.SetArgs([]string{"script", "extract", "-i", cutScriptPath, "-o", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("script extract fallback failed: %v", err)
	}

	var extracted model.ExtractedScript
	if err := json.Unmarshal(stdoutBuf.Bytes(), &extracted); err != nil {
		t.Fatalf("stdout should still contain clean JSON: %v\nstdout:\n%s", err, stdoutBuf.String())
	}
	if len(extracted.Dialogue) != 2 ||
		extracted.Dialogue[0].Text != "（コマンドで切れた" ||
		extracted.Dialogue[1].Text != "残り）" {
		t.Fatalf("unexpected fallback dialogue units: %#v", extracted.Dialogue)
	}
	if !strings.Contains(stderrBuf.String(), "warning:") {
		t.Fatalf("expected stderr warning for split dialogue block, got %q", stderrBuf.String())
	}
}

func TestCLI_DefaultInit(t *testing.T) {
	workDir := t.TempDir()
	fakeUserConfig := filepath.Join(workDir, "xdg-config")
	wantCfgPath := filepath.Join(fakeUserConfig, "script-translator", "config.json")

	runInit := func(args ...string) (string, error) {
		var out bytes.Buffer
		cmd := newRootCmd(cliDeps{
			stdin:         strings.NewReader(""),
			stdout:        &out,
			stderr:        &bytes.Buffer{},
			stdinIsTTY:    func() bool { return true },
			userConfigDir: func() (string, error) { return fakeUserConfig, nil },
		})
		cmd.SetArgs(append([]string{"default", "init"}, args...))
		err := cmd.Execute()
		return out.String(), err
	}

	out, err := runInit()
	if err != nil {
		t.Fatalf("default init failed: %v", err)
	}
	if !strings.Contains(out, wantCfgPath) {
		t.Fatalf("expected output to mention %q, got %q", wantCfgPath, out)
	}

	raw, err := os.ReadFile(wantCfgPath)
	if err != nil {
		t.Fatalf("read initialized config: %v", err)
	}
	// Minimal file should omit optional defaults and stay concise.
	if strings.Contains(string(raw), "observation") || strings.Contains(string(raw), "provider") {
		t.Fatalf("expected minimal config template without optional blocks, got:\n%s", string(raw))
	}

	// Initialized config must pass config.Load and apply all defaults cleanly.
	loaded, err := config.Load(wantCfgPath)
	if err != nil {
		t.Fatalf("config.Load on initialized config failed: %v", err)
	}
	if loaded.EntityRecognition.Model != defaultTemplateModel || loaded.Translation.Model != defaultTemplateModel {
		t.Fatalf("unexpected template models: %+v", loaded)
	}
	if loaded.CacheDir != config.DefaultCacheDir || loaded.Provider.APIKeyEnv != config.DefaultAPIKeyEnv || !loaded.Observation.Log.Console.Enable {
		t.Fatalf("expected default settings applied after Load: %+v", loaded)
	}

	// Re-running without --force refuses to overwrite customized file.
	customContent := []byte("{\"custom\":true}\n")
	if err := os.WriteFile(wantCfgPath, customContent, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := runInit(); err == nil {
		t.Fatal("expected error when config already exists without --force")
	}
	afterSkip, err := os.ReadFile(wantCfgPath)
	if err != nil || string(afterSkip) != string(customContent) {
		t.Fatalf("expected existing file untouched without --force, got %q (err=%v)", string(afterSkip), err)
	}

	// Passing --force overwrites with fresh minimal template.
	if _, err := runInit("--force"); err != nil {
		t.Fatalf("default init --force failed: %v", err)
	}
	if _, err := config.Load(wantCfgPath); err != nil {
		t.Fatalf("config.Load after --force failed: %v", err)
	}
}
