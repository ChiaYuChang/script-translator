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
			if len(units) != 2 {
				t.Fatalf("expected 2 units, got %d", len(units))
			}
			b, err := json.Marshal(map[string]any{
				"dialogue": []map[string]any{
					{"index": units[0].Index, "text": []string{"去秋葉原吧"}},
					{"index": units[1].Index, "text": []string{"傍晚的教室。"}},
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

func TestPipelineRecognizePipesToTranslateAndEntityInsert(t *testing.T) {
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

	logPath := filepath.Join(dir, "logs.jsonl")
	metricPath := filepath.Join(dir, "metrics.jsonl")
	cfgPath := writeTestConfig(t, dir, srv.URL+"/v1", logPath, metricPath)

	// Step 1: pipeline recognize -> stdout (stderr keeps logs isolated).
	var recStdout, recStderr bytes.Buffer
	recCmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{},
		stdout:     &recStdout,
		stderr:     &recStderr,
		stdinIsTTY: func() bool { return true },
	})
	recCmd.SetArgs([]string{
		"pipeline", "recognize",
		"-c", cfgPath,
		"-i", scriptPath,
		"-d", dictPath,
	})
	if err := recCmd.Execute(); err != nil {
		t.Fatalf("pipeline recognize failed: %v\nstderr:\n%s", err, recStderr.String())
	}

	payload, err := decodeRecognizePayload(recStdout.Bytes())
	if err != nil {
		t.Fatalf("decode recognize stdout: %v\nraw:\n%s", err, recStdout.String())
	}
	if payload.Input != scriptPath || len(payload.Entities) != 2 {
		t.Fatalf("unexpected recognize payload: %#v", payload)
	}

	// Step 2a: pipe recognize output -> pipeline translate (no -i needed).
	outScriptPath := filepath.Join(dir, "translated.txt")
	var trStdout, trStderr bytes.Buffer
	trCmd := newRootCmd(cliDeps{
		stdin:      bytes.NewReader(recStdout.Bytes()),
		stdout:     &trStdout,
		stderr:     &trStderr,
		stdinIsTTY: func() bool { return false },
	})
	trCmd.SetArgs([]string{
		"pipeline", "translate",
		"-c", cfgPath,
		"-o", outScriptPath,
	})
	if err := trCmd.Execute(); err != nil {
		t.Fatalf("pipeline translate failed: %v\nstderr:\n%s", err, trStderr.String())
	}

	translatedBytes, err := os.ReadFile(outScriptPath)
	if err != nil {
		t.Fatal(err)
	}
	translatedText := string(translatedBytes)
	if !strings.Contains(translatedText, "<NAME_PLATE>新堂湊") ||
		!strings.Contains(translatedText, "「去秋葉原吧」") ||
		!strings.Contains(translatedText, "傍晚的教室。") {
		t.Fatalf("unexpected translated script:\n%s", translatedText)
	}

	// Step 2b: pipe recognize output -> entity insert, then list.
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

func TestPipelineTranslateStandaloneWithDictEvenWhenNonTTYStdin(t *testing.T) {
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

	var stdout, stderr bytes.Buffer
	cmd := newRootCmd(cliDeps{
		stdin:      &bytes.Buffer{}, // simulates empty /dev/null non-TTY stdin
		stdout:     &stdout,
		stderr:     &stderr,
		stdinIsTTY: func() bool { return false },
	})
	cmd.SetArgs([]string{
		"pipeline", "translate",
		"-c", cfgPath,
		"-i", scriptPath,
		"-d", dictPath,
	})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("standalone translate with --dict failed: %v", err)
	}
	if !strings.Contains(stdout.String(), "<NAME_PLATE>新堂湊") {
		t.Fatalf("expected translated output on stdout, got:\n%s", stdout.String())
	}
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
