package e2e_test

import (
	"bytes"
	"encoding/json/v2"
	"flag"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"go-script-translate/internal/config"
	"go-script-translate/internal/entitystore"
	"go-script-translate/internal/model"
)

var (
	liveAPI        = flag.Bool("live", false, "run E2E against the real OpenRouter API configured in .env and e2e/*/config.json")
	recordFixtures = flag.Bool("record", false, "when -live is enabled, save raw HTTP responses to e2e/fixtures/")
)

func TestEndToEndPipeline(t *testing.T) {
	e2eDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs(filepath.Join(e2eDir, ".."))
	if err != nil {
		t.Fatal(err)
	}

	binPath := filepath.Join(t.TempDir(), "translate")
	buildCmd := exec.Command("go", "build", "-o", binPath, "./cmd/translate")
	buildCmd.Dir = repoRoot
	if out, err := buildCmd.CombinedOutput(); err != nil {
		t.Fatalf("build CLI binary: %v\n%s", err, string(out))
	}

	workDir := t.TempDir()
	scriptPath := filepath.Join(e2eDir, "data", "e2e_scene.txt")
	seedPath := filepath.Join(e2eDir, "data", "seed_entities.json")
	dictPath := filepath.Join(workDir, "entity_dict.json")
	copyFile(t, seedPath, dictPath)

	recLogPath := filepath.Join(workDir, "recognize_logs.jsonl")
	recMetricPath := filepath.Join(workDir, "recognize_metrics.jsonl")
	trLogPath := filepath.Join(workDir, "translate_logs.jsonl")
	trMetricPath := filepath.Join(workDir, "translate_metrics.jsonl")

	var recConfigPath, trConfigPath string
	var extraEnv []string
	var rawDir string
	fixturesDir := filepath.Join(e2eDir, "fixtures")

	if *liveAPI {
		extraEnv = loadDotEnvIfNeeded(t, filepath.Join(repoRoot, ".env"), "OPENROUTER_APIKEY")
		recConfigPath = rewriteConfigForTempObs(t, filepath.Join(e2eDir, "recognize", "config.json"), workDir, "recognize", "", "")
		trConfigPath = rewriteConfigForTempObs(t, filepath.Join(e2eDir, "translate", "config.json"), workDir, "translate", "", "")
		if *recordFixtures {
			clearFixtures(t, fixturesDir)
			rawDir = fixturesDir
		}
	} else {
		srv := startFixtureServer(t, fixturesDir)
		defer srv.Close()
		baseURL := srv.URL + "/v1"
		recConfigPath = rewriteConfigForTempObs(t, filepath.Join(e2eDir, "recognize", "config.json"), workDir, "recognize", baseURL, "test-fixture-key")
		trConfigPath = rewriteConfigForTempObs(t, filepath.Join(e2eDir, "translate", "config.json"), workDir, "translate", baseURL, "test-fixture-key")
	}

	// 1. Staged pipeline recognize -> recognized.json
	recognizedPath := filepath.Join(workDir, "recognized.json")
	recArgs := []string{
		"pipeline", "recognize",
		"-c", recConfigPath,
		"-i", scriptPath,
		"-d", dictPath,
		"-o", recognizedPath,
	}
	if rawDir != "" {
		recArgs = append(recArgs, "--save-raw", rawDir)
	}
	runCLI(t, binPath, repoRoot, extraEnv, nil, recArgs...)

	recBytes, err := os.ReadFile(recognizedPath)
	if err != nil {
		t.Fatal(err)
	}
	var recOut struct {
		Input    string         `json:"input"`
		Entities []model.Entity `json:"entities"`
	}
	if err := json.Unmarshal(recBytes, &recOut); err != nil {
		t.Fatalf("decode recognized.json: %v\n%s", err, string(recBytes))
	}
	if len(recOut.Entities) < 4 {
		t.Fatalf("expected seeded entities plus newly discovered entities, got %d: %#v", len(recOut.Entities), recOut.Entities)
	}

	// 2. Staged pipeline translate -> translated.txt
	translatedPath := filepath.Join(workDir, "translated.txt")
	trArgs := []string{
		"pipeline", "translate",
		"-c", trConfigPath,
		"-e", recognizedPath,
		"-o", translatedPath,
	}
	if rawDir != "" {
		trArgs = append(trArgs, "--save-raw", rawDir)
	}
	runCLI(t, binPath, repoRoot, extraEnv, nil, trArgs...)

	translatedBytes, err := os.ReadFile(translatedPath)
	if err != nil {
		t.Fatal(err)
	}
	translatedText := string(translatedBytes)
	for _, wantSnippet := range []string{
		"<BGM_PLAY>bgm_evening_walk,1000",
		"<SE_PLAY>se_train_door",
		"<WAIT>300",
		"<VOICE_PLAY>vo_minato_001",
		"<ACTOR_FRONT>1",
		"<EFFECT_FLASH>WHITE,200",
		"<SPINE>sp_ren_idle,CENTER",
		"<SPINE_ANIMATOR>wave",
		"<EFFECT_QUAKE>SMALL,300",
		"<EV>ev_old_script,IN,600",
		"<EV_OUT>400",
		"<GROUP>",
		"</GROUP>",
		"<NAME_PLATE>新堂湊",
		"<NAME_PLATE>朝倉由依",
		"<SCENARIO_END>",
	} {
		if !strings.Contains(translatedText, wantSnippet) {
			t.Fatalf("translated script missing %q:\n%s", wantSnippet, translatedText)
		}
	}

	// 3. Entity insert & list verification.
	runCLI(t, binPath, repoRoot, extraEnv, nil,
		"entity", "insert",
		"-d", dictPath,
		"-i", recognizedPath,
	)
	listOut := runCLI(t, binPath, repoRoot, extraEnv, nil,
		"entity", "list",
		"-d", dictPath,
		"--json",
	)
	var listed model.EntityMapping
	if err := json.Unmarshal([]byte(listOut), &listed); err != nil {
		t.Fatalf("decode entity list --json: %v", err)
	}
	if len(listed.Entities) != len(recOut.Entities) {
		t.Fatalf("expected %d entities after insert, got %d", len(recOut.Entities), len(listed.Entities))
	}

	// 4. Verify observation logs and metrics written during staged runs.
	assertJSONLFile(t, recLogPath, 1)
	assertMetricsTypes(t, recMetricPath, "entity_usage", "total_usage")
	assertJSONLFile(t, trLogPath, 1)
	assertMetricsTypes(t, trMetricPath, "translation_batch_usage", "total_usage")

	// 5. In fixture mode, also verify true OS Unix pipe execution (`recognize | translate`).
	if !*liveAPI {
		pipeOut := runCLI(t, binPath, repoRoot, extraEnv, bytes.NewReader(recBytes),
			"pipeline", "translate",
			"-c", trConfigPath,
		)
		if pipeOut != translatedText {
			t.Fatalf("piped translate output differed from file-based translate")
		}
	}
}

func startFixtureServer(t *testing.T, fixturesDir string) *httptest.Server {
	t.Helper()
	recFixtures := loadFixtureFiles(t, filepath.Join(fixturesDir, "recognize_*.json"))
	trFixtures := loadFixtureFiles(t, filepath.Join(fixturesDir, "translate_*.json"))
	if len(recFixtures) == 0 || len(trFixtures) == 0 {
		t.Fatalf("missing E2E fixtures in %s (run `go test ./e2e -live -record` to capture them)", fixturesDir)
	}

	var mu sync.Mutex
	var recIdx, trIdx int

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
			http.Error(w, "unexpected path", http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var req struct {
			Input string `json:"input"`
			Text  struct {
				Format struct {
					Name string `json:"name"`
				} `json:"format"`
			} `json:"text"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Errorf("unmarshal request body: %v", err)
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}

		mu.Lock()
		defer mu.Unlock()

		var respBytes []byte
		switch req.Text.Format.Name {
		case "entity_recognition":
			if !strings.Contains(req.Input, "KnownEntities:\n") || !strings.Contains(req.Input, "\nTranscript:\n") {
				t.Errorf("recognition request missing KnownEntities/Transcript:\n%s", req.Input)
				http.Error(w, "invalid recognition request", http.StatusBadRequest)
				return
			}
			if recIdx >= len(recFixtures) {
				t.Errorf("unexpected extra entity_recognition request #%d", recIdx+1)
				http.Error(w, "unexpected extra request", http.StatusBadRequest)
				return
			}
			respBytes = recFixtures[recIdx]
			recIdx++
		case "script_translation":
			if !strings.Contains(req.Input, "Entries:\n") || !strings.Contains(req.Input, "\nInput:\n") {
				t.Errorf("translation request missing Entries/Input:\n%s", req.Input)
				http.Error(w, "invalid translation request", http.StatusBadRequest)
				return
			}
			batchIdx := trIdx % len(trFixtures)
			if batchIdx > 0 && !strings.Contains(req.Input, "\nHistory:\n") {
				t.Errorf("expected History section on translation batch %d", batchIdx+1)
				http.Error(w, "missing History section", http.StatusBadRequest)
				return
			}
			respBytes = trFixtures[batchIdx]
			trIdx++
		default:
			t.Fatalf("unexpected schema name %q", req.Text.Format.Name)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(respBytes)
	}))
}

func loadFixtureFiles(t *testing.T, pattern string) [][]byte {
	t.Helper()
	paths, err := filepath.Glob(pattern)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	out := make([][]byte, 0, len(paths))
	for _, p := range paths {
		if strings.Contains(filepath.Base(p), "_err_") {
			continue
		}
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, b)
	}
	return out
}

func clearFixtures(t *testing.T, fixturesDir string) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(fixturesDir, "*.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
}

func rewriteConfigForTempObs(t *testing.T, srcPath, workDir, prefix, overrideBaseURL, overrideAPIKey string) string {
	t.Helper()
	cfg, err := config.Load(srcPath)
	if err != nil {
		t.Fatalf("load template config %s: %v", srcPath, err)
	}
	if overrideBaseURL != "" {
		cfg.Provider.BaseURL = overrideBaseURL
	}
	if overrideAPIKey != "" {
		cfg.Provider.APIKey = overrideAPIKey
	}
	cfg.Observation.Log.Console.Enable = false
	cfg.Observation.Log.File.Enable = true
	cfg.Observation.Log.File.File = filepath.Join(workDir, prefix+"_logs.jsonl")
	cfg.Observation.Metric.Enable = true
	cfg.Observation.Metric.File = filepath.Join(workDir, prefix+"_metrics.jsonl")

	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(workDir, prefix+"_config.json")
	if err := os.WriteFile(outPath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return outPath
}

func loadDotEnvIfNeeded(t *testing.T, envPath, key string) []string {
	t.Helper()
	if os.Getenv(key) != "" {
		return nil
	}
	b, err := os.ReadFile(envPath)
	if err != nil {
		t.Fatalf("%s is not set and reading %s failed: %v", key, envPath, err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(k) == key {
			v = strings.Trim(strings.TrimSpace(v), `"'`)
			if v != "" {
				return []string{key + "=" + v}
			}
		}
	}
	t.Fatalf("%s missing in environment and %s", key, envPath)
	return nil
}

func runCLI(t *testing.T, binPath, dir string, extraEnv []string, stdin io.Reader, args ...string) string {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("command %v failed: %v\nstderr:\n%s\nstdout:\n%s", args, err, stderr.String(), stdout.String())
	}
	return stdout.String()
}

func copyFile(t *testing.T, src, dst string) {
	t.Helper()
	mapping, err := entitystore.Load(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := entitystore.Save(dst, mapping); err != nil {
		t.Fatal(err)
	}
}

func assertJSONLFile(t *testing.T, path string, minLines int) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < minLines {
		t.Fatalf("%s: expected at least %d lines, got %d", path, minLines, len(lines))
	}
	for i, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("%s line %d invalid JSON: %v", path, i+1, err)
		}
		if entry["run_id"] == "" || entry["run_id"] == nil {
			t.Fatalf("%s line %d missing run_id", path, i+1)
		}
	}
}

func assertMetricsTypes(t *testing.T, path, wantCallType, wantFinalType string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) < 2 {
		t.Fatalf("%s: expected >=2 metric lines, got %d", path, len(lines))
	}
	var sawCall bool
	for i, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("%s line %d invalid JSON: %v", path, i+1, err)
		}
		if ev["type"] == wantCallType {
			sawCall = true
		}
		if i == len(lines)-1 && ev["type"] != wantFinalType {
			t.Fatalf("%s last line type = %v, want %q", path, ev["type"], wantFinalType)
		}
	}
	if !sawCall {
		t.Fatalf("%s missing expected metric type %q", path, wantCallType)
	}
}
