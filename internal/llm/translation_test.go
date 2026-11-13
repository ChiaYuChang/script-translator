package llm

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"go-script-translate/internal/model"
	"go-script-translate/internal/observation"
)

type capturedResponseRequest struct {
	Model        string `json:"model"`
	Instructions string `json:"instructions"`
	Input        string `json:"input"`
	Text         struct {
		Format struct {
			Type   string         `json:"type"`
			Name   string         `json:"name"`
			Strict bool           `json:"strict"`
			Schema map[string]any `json:"schema"`
		} `json:"format"`
	} `json:"text"`
}

func defaultTestTranslateParams() TranslateParams {
	return TranslateParams{
		Model:      "gpt-test",
		BatchSize:  2,
		MaxAttempt: 1,
	}
}

func newTestProvider(t *testing.T, handler http.HandlerFunc, opts ...ProviderOption) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := openai.NewClient(
		option.WithBaseURL(srv.URL+"/v1"),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	return NewProvider(&client, opts...), srv
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, status string, contentItem map[string]any) {
	writeJSONResponseWithUsage(t, w, status, contentItem, nil)
}

func writeJSONResponseWithUsage(t *testing.T, w http.ResponseWriter, status string, contentItem map[string]any, usage map[string]any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]any{
		"id":     "resp_test",
		"object": "response",
		"status": status,
		"output": []map[string]any{
			{
				"id":      "msg_test",
				"type":    "message",
				"status":  "completed",
				"role":    "assistant",
				"content": []map[string]any{contentItem},
			},
		},
	}
	if usage != nil {
		resp["usage"] = usage
	}
	if err := json.MarshalWrite(w, resp); err != nil {
		t.Fatalf("encode mock response: %v", err)
	}
}

func decodeCapturedRequest(t *testing.T, r *http.Request) capturedResponseRequest {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" {
		t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	var req capturedResponseRequest
	if err := json.Unmarshal(body, &req); err != nil {
		t.Fatalf("unmarshal request body: %v\nbody: %s", err, string(body))
	}
	return req
}

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return string(b)
}

func extractEntriesSection(t *testing.T, prompt string) string {
	t.Helper()
	const prefix = "Entries:\n"
	if !strings.HasPrefix(prompt, prefix) {
		t.Fatalf("prompt missing Entries header:\n%s", prompt)
	}
	rest := prompt[len(prefix):]
	if strings.HasPrefix(rest, "\n") {
		return ""
	}
	end := strings.Index(rest, "\n\n")
	if end < 0 {
		t.Fatalf("prompt missing section separator after Entries:\n%s", prompt)
	}
	return rest[:end]
}

func parseInputSection(t *testing.T, prompt string) []model.TranslateUnit {
	t.Helper()
	const marker = "\n\nInput:\n"
	idx := strings.Index(prompt, marker)
	if idx < 0 {
		t.Fatalf("prompt missing Input section:\n%s", prompt)
	}
	var units []model.TranslateUnit
	if err := json.Unmarshal([]byte(prompt[idx+len(marker):]), &units); err != nil {
		t.Fatalf("unmarshal Input section: %v", err)
	}
	return units
}

func TestTranslateValidationGuards(t *testing.T) {
	var calls atomic.Int32
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})

	valid := defaultTestTranslateParams()

	if _, err := (*Provider)(nil).Translate(context.Background(), model.TranslateRequest{}, valid); err == nil {
		t.Fatal("expected error for nil provider")
	}
	if _, err := NewProvider(nil).Translate(context.Background(), model.TranslateRequest{}, valid); err == nil {
		t.Fatal("expected error for nil client")
	}
	if _, err := p.Translate(context.Background(), model.TranslateRequest{}, TranslateParams{Model: "  ", BatchSize: 2, MaxAttempt: 1}); err == nil {
		t.Fatal("expected error for empty model name")
	}
	if _, err := p.Translate(context.Background(), model.TranslateRequest{}, TranslateParams{Model: "gpt-test", BatchSize: 0, MaxAttempt: 1}); err == nil {
		t.Fatal("expected error for zero batch size")
	}
	if _, err := p.Translate(context.Background(), model.TranslateRequest{}, TranslateParams{Model: "gpt-test", BatchSize: -3, MaxAttempt: 1}); err == nil {
		t.Fatal("expected error for negative batch size")
	}
	if _, err := p.Translate(context.Background(), model.TranslateRequest{}, TranslateParams{Model: "gpt-test", BatchSize: 2, MaxAttempt: 0}); err == nil {
		t.Fatal("expected error for zero max attempt")
	}
	if _, err := p.Translate(context.Background(), model.TranslateRequest{}, TranslateParams{Model: "gpt-test", BatchSize: 2, MaxAttempt: -1}); err == nil {
		t.Fatal("expected error for negative max attempt")
	}

	got, err := p.Translate(context.Background(), model.TranslateRequest{}, valid)
	if err != nil {
		t.Fatalf("unexpected error for empty dialogue: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty response, got %#v", got)
	}
	if calls.Load() != 0 {
		t.Fatalf("expected 0 API calls for guard/empty checks, got %d", calls.Load())
	}
}

func TestTranslateSingleBatchReordersAndAllowsTextResegmentation(t *testing.T) {
	var captured capturedResponseRequest
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeCapturedRequest(t, r)
		// Return indices reversed and with different text array lengths.
		out := mustMarshalJSON(t, translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: 25, Text: []string{"合併成一句的翻譯"}},
				{Index: 12, Text: []string{"第一句", "第二句", "第三句"}},
			},
		})
		writeJSONResponse(t, w, "completed", map[string]any{
			"type": "output_text",
			"text": out,
		})
	})

	req := model.TranslateRequest{
		Entity: []model.Entity{
			{ID: 1, Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"}},
		},
		Dialogue: []model.TranslateUnit{
			{Index: 12, Speaker: "新堂ミナト", Text: []string{"おはよう。"}},
			{Index: 25, Speaker: "", Text: []string{"朝の日差しが差す。", "鳥が鳴いている。"}},
		},
	}

	got, err := p.Translate(context.Background(), req, TranslateParams{
		Model:      "gpt-custom",
		BatchSize:  5,
		MaxAttempt: 1,
	})
	if err != nil {
		t.Fatalf("Translate failed: %v", err)
	}

	want := model.TranslateResponse{
		{Index: 12, Text: []string{"第一句", "第二句", "第三句"}},
		{Index: 25, Text: []string{"合併成一句的翻譯"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("response mismatch:\ngot  %#v\nwant %#v", got, want)
	}

	if captured.Model != "gpt-custom" {
		t.Fatalf("unexpected model: %q", captured.Model)
	}
	if captured.Instructions != TranslationPrompt {
		t.Fatal("instructions mismatch")
	}
	if captured.Text.Format.Type != "json_schema" || !captured.Text.Format.Strict || captured.Text.Format.Name != "script_translation" {
		t.Fatalf("unexpected text format: %#v", captured.Text.Format)
	}
	if !strings.HasPrefix(captured.Input, "Entries:\n") {
		t.Fatalf("expected Entries section at start:\n%s", captured.Input)
	}
	if strings.Contains(captured.Input, "History:") {
		t.Fatalf("first batch should omit History section:\n%s", captured.Input)
	}
	if units := parseInputSection(t, captured.Input); !reflect.DeepEqual(units, req.Dialogue) {
		t.Fatalf("Input section mismatch:\ngot  %#v\nwant %#v", units, req.Dialogue)
	}
}

func TestTranslateMultiBatchHistoryAndEntries(t *testing.T) {
	var requests []capturedResponseRequest
	batchResponses := []string{
		mustMarshalJSON(t, translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: 10, Text: []string{"湊說早安"}},
				{Index: 20, Text: []string{"由依回早安"}},
			},
		}),
		mustMarshalJSON(t, translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: 35, Text: []string{"他們走出教室"}},
				{Index: 30, Text: []string{"鈴聲響起了"}},
			},
		}),
		mustMarshalJSON(t, translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: 42, Text: []string{"放學了。"}},
			},
		}),
	}

	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeCapturedRequest(t, r)
		idx := len(requests)
		requests = append(requests, req)
		if idx >= len(batchResponses) {
			t.Fatalf("unexpected extra request %d", idx+1)
		}
		writeJSONResponse(t, w, "completed", map[string]any{
			"type": "output_text",
			"text": batchResponses[idx],
		})
	})

	req := model.TranslateRequest{
		Entity: []model.Entity{
			{
				ID:        1,
				Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
				Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
			},
		},
		Dialogue: []model.TranslateUnit{
			{Index: 10, Speaker: "新堂ミナト", Text: []string{"おはよう"}},
			{Index: 20, Speaker: "朝倉ユイ", Text: []string{"おはよう、ミナト"}},
			{Index: 30, Speaker: "", Text: []string{"チャイムが鳴る。"}},
			{Index: 35, Speaker: "", Text: []string{"二人は教室を出た。"}},
			{Index: 42, Speaker: "新堂ミナト", Text: []string{"もう放課後か。"}},
		},
	}

	got, err := p.Translate(context.Background(), req, defaultTestTranslateParams())
	if err != nil {
		t.Fatalf("Translate failed: %v", err)
	}

	want := model.TranslateResponse{
		{Index: 10, Text: []string{"湊說早安"}},
		{Index: 20, Text: []string{"由依回早安"}},
		{Index: 30, Text: []string{"鈴聲響起了"}},
		{Index: 35, Text: []string{"他們走出教室"}},
		{Index: 42, Text: []string{"放學了。"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged mismatch:\ngot  %#v\nwant %#v", got, want)
	}

	if len(requests) != 3 {
		t.Fatalf("expected 3 batch requests, got %d", len(requests))
	}

	// All batches include the formatted Entries glossary.
	wantEntries := req.Entity[0].String()
	for i, r := range requests {
		gotEntries := extractEntriesSection(t, r.Input)
		if gotEntries != wantEntries {
			t.Fatalf("request %d Entries mismatch:\ngot:\n%s\nwant:\n%s", i+1, gotEntries, wantEntries)
		}
	}

	// Batch 1 omits History; Batch 2 & 3 contain compact Speaker:Text transcript.
	if strings.Contains(requests[0].Input, "History:") {
		t.Fatalf("batch 1 should not have History:\n%s", requests[0].Input)
	}

	wantHistory2 := "History:\n新堂ミナト:湊說早安\n朝倉ユイ:由依回早安\n\nInput:\n"
	if !strings.Contains(requests[1].Input, wantHistory2) {
		t.Fatalf("batch 2 History mismatch:\ngot:\n%s\nwant substring:\n%s", requests[1].Input, wantHistory2)
	}

	wantHistory3 := "History:\n新堂ミナト:湊說早安\n朝倉ユイ:由依回早安\nOS:鈴聲響起了\nOS:他們走出教室\n\nInput:\n"
	if !strings.Contains(requests[2].Input, wantHistory3) {
		t.Fatalf("batch 3 History mismatch:\ngot:\n%s\nwant substring:\n%s", requests[2].Input, wantHistory3)
	}

	if units := parseInputSection(t, requests[2].Input); len(units) != 1 || units[0].Index != 42 {
		t.Fatalf("batch 3 Input mismatch: %#v", units)
	}
}

func TestTranslateNoHistoryPollutionAcrossCalls(t *testing.T) {
	var requests []capturedResponseRequest
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeCapturedRequest(t, r)
		requests = append(requests, req)
		units := parseInputSection(t, req.Input)
		resp := translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: units[0].Index, Text: []string{"已翻譯"}},
			},
		}
		b, _ := json.Marshal(resp)
		writeJSONResponse(t, w, "completed", map[string]any{
			"type": "output_text",
			"text": string(b),
		})
	})

	req1 := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 1, Text: []string{"一"}}},
	}
	req2 := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 2, Text: []string{"二"}}},
	}

	params := TranslateParams{Model: "gpt-test", BatchSize: 1, MaxAttempt: 1}
	if _, err := p.Translate(context.Background(), req1, params); err != nil {
		t.Fatalf("first Translate failed: %v", err)
	}
	if _, err := p.Translate(context.Background(), req2, params); err != nil {
		t.Fatalf("second Translate failed: %v", err)
	}

	if len(requests) != 2 {
		t.Fatalf("expected 2 requests, got %d", len(requests))
	}
	if strings.Contains(requests[1].Input, "History:") {
		t.Fatalf("second Translate call polluted with previous history:\n%s", requests[1].Input)
	}
	if gotEntries := extractEntriesSection(t, requests[1].Input); gotEntries != "" {
		t.Fatalf("expected empty Entries section, got %q", gotEntries)
	}
}

func TestTranslateRetriesWithinMaxAttemptAndSucceeds(t *testing.T) {
	var calls atomic.Int32
	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		switch n {
		case 1:
			// Batch 1 attempt 1: invalid JSON -> should retry.
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue": [`,
			})
		case 2:
			// Batch 1 attempt 2: missing index -> should retry.
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue":[{"text":["錯"]}]}`,
			})
		case 3:
			// Batch 1 attempt 3: valid.
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue":[{"index":0,"text":["第一句"]}]}`,
			})
		case 4:
			// Batch 2 attempt 1: valid.
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue":[{"index":1,"text":["第二句"]}]}`,
			})
		default:
			t.Fatalf("unexpected call %d", n)
		}
	})

	req := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{
			{Index: 0, Speaker: "A", Text: []string{"一"}},
			{Index: 1, Speaker: "B", Text: []string{"二"}},
		},
	}

	got, err := p.Translate(context.Background(), req, TranslateParams{
		Model:      "gpt-test",
		BatchSize:  1,
		MaxAttempt: 3,
	})
	if err != nil {
		t.Fatalf("Translate failed: %v", err)
	}
	want := model.TranslateResponse{
		{Index: 0, Text: []string{"第一句"}},
		{Index: 1, Text: []string{"第二句"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	if calls.Load() != 4 {
		t.Fatalf("expected 4 total API calls (3 for batch 1, 1 for batch 2), got %d", calls.Load())
	}
}

func TestTranslateValidationErrorsAndFailFast(t *testing.T) {
	cases := []struct {
		name string
		resp func(w http.ResponseWriter)
	}{
		{
			name: "missing index",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":["ok"]}]}`,
				})
			},
		},
		{
			name: "duplicate index",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":["a"]},{"index":3,"text":["b"]}]}`,
				})
			},
		},
		{
			name: "unknown index",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":["a"]},{"index":999,"text":["b"]}]}`,
				})
			},
		},
		{
			name: "null text",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":null},{"index":4,"text":["b"]}]}`,
				})
			},
		},
		{
			name: "empty text array",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":[]},{"index":4,"text":["b"]}]}`,
				})
			},
		},
		{
			name: "whitespace only text",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":["  \t\n "]},{"index":4,"text":["b"]}]}`,
				})
			},
		},
		{
			name: "blank line inside text array",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":["ok","  "]},{"index":4,"text":["b"]}]}`,
				})
			},
		},
		{
			name: "missing dialogue field",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{}`,
				})
			},
		},
		{
			name: "invalid JSON",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue": [`,
				})
			},
		},
		{
			name: "refusal",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type":    "refusal",
					"refusal": "cannot comply",
				})
			},
		},
		{
			name: "incomplete status",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "incomplete", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":["a"]},{"index":4,"text":["b"]}]}`,
				})
			},
		},
		{
			name: "API error",
			resp: func(w http.ResponseWriter) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"error":{"message":"bad request","type":"invalid_request_error"}}`))
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					writeJSONResponse(t, w, "completed", map[string]any{
						"type": "output_text",
						"text": `{"dialogue":[{"index":1,"text":["一"]},{"index":2,"text":["二"]}]}`,
					})
					return
				}
				if n <= 3 {
					// Batch 2 fails for both attempts (MaxAttempt = 2).
					tc.resp(w)
					return
				}
				t.Fatalf("unexpected call %d after batch 2 exhausted attempts", n)
			})

			req := model.TranslateRequest{
				Dialogue: []model.TranslateUnit{
					{Index: 1, Text: []string{"1"}},
					{Index: 2, Text: []string{"2"}},
					{Index: 3, Text: []string{"3"}},
					{Index: 4, Text: []string{"4"}},
					{Index: 5, Text: []string{"5"}},
				},
			}
			_, err := p.Translate(context.Background(), req, TranslateParams{
				Model:      "gpt-test",
				BatchSize:  2,
				MaxAttempt: 2,
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), "batch 2") || !strings.Contains(err.Error(), "2 attempts") {
				t.Fatalf("expected error to mention batch 2 and 2 attempts, got: %v", err)
			}
			if calls.Load() != 3 {
				t.Fatalf("expected 1 call for batch 1 + 2 attempts for batch 2 (3 calls), got %d calls", calls.Load())
			}
		})
	}
}

func TestTranslateContextCancellationDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var calls atomic.Int32

	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			cancel()
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue":[{"index":1,"text":["一"]}]}`,
			})
			return
		}
		t.Fatal("server should not receive batch 2 after context cancellation")
	})

	req := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{
			{Index: 1, Text: []string{"1"}},
			{Index: 2, Text: []string{"2"}},
		},
	}

	_, err := p.Translate(ctx, req, TranslateParams{
		Model:      "gpt-test",
		BatchSize:  1,
		MaxAttempt: 3,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call before cancel, got %d", calls.Load())
	}
}

func TestTranslateRejectsMissingIndexEvenWhenInputIsZero(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{
			name: "omitted index",
			body: `{"dialogue":[{"text":["早安"]}]}`,
		},
		{
			name: "null index",
			body: `{"dialogue":[{"index":null,"text":["早安"]}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": tc.body,
				})
			})

			req := model.TranslateRequest{
				Dialogue: []model.TranslateUnit{
					{Index: 0, Speaker: "新堂ミナト", Text: []string{"おはよう"}},
				},
			}

			_, err := p.Translate(context.Background(), req, TranslateParams{
				Model:      "gpt-test",
				BatchSize:  1,
				MaxAttempt: 1,
			})
			if err == nil {
				t.Fatal("expected error when index field is missing or null")
			}
			if !strings.Contains(err.Error(), "index") {
				t.Fatalf("expected index validation error, got: %v", err)
			}
		})
	}
}

func TestTranslationSchema(t *testing.T) {
	schemaMap, err := ToSchemaMap(translationSchema)
	if err != nil {
		t.Fatalf("ToSchemaMap(translationSchema): %v", err)
	}
	if len(schemaMap) == 0 {
		t.Fatal("expected non-empty translation schema map")
	}

	resolved, err := translationSchema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve translation schema: %v", err)
	}

	validBytes, err := json.Marshal(translationOutput{
		Dialogue: model.TranslateBatchOutput{
			{Index: 0, Text: []string{"早安"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateJSON(resolved, string(validBytes)); err != nil {
		t.Fatalf("expected valid translation payload to pass schema: %v", err)
	}

	for _, invalid := range []string{
		`{}`,
		`{"dialogue":null}`,
		`{"dialogue":[{"text":["缺 index"]}]}`,
		`{"dialogue":[{"index":null,"text":["null index"]}]}`,
		`{"dialogue":[{"index":0,"text":null}]}`,
		`{"dialogue":[{"index":0,"text":["ok"],"extra":true}]}`,
	} {
		if err := ValidateJSON(resolved, invalid); err == nil {
			t.Errorf("expected invalid translation payload to fail schema: %s", invalid)
		}
	}
}

func TestProviderRecordsMetricsAndLogsAcrossRetries(t *testing.T) {
	var calls atomic.Int32
	const runID = "01950000-0000-7000-8000-000000000001"
	var metricBuf bytes.Buffer
	metrics := observation.NewMetrics(runID, &metricBuf)
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With(slog.String("run_id", runID))

	p, _ := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			// Entity recognition succeeds.
			writeJSONResponseWithUsage(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": mustMarshalJSON(t, recognitionOutput{
					Entity: []recognizedEntity{{
						Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"},
						Aliases:   []model.NamePair{},
					}},
				}),
			}, map[string]any{
				"input_tokens":  50,
				"output_tokens": 15,
				"total_tokens":  65,
				"input_tokens_details": map[string]any{
					"cached_tokens": 10,
				},
				"output_tokens_details": map[string]any{
					"reasoning_tokens": 5,
				},
			})
		case 2:
			// Translation attempt 1 fails schema validation, but usage is still recorded.
			writeJSONResponseWithUsage(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue":[]}`,
			}, map[string]any{
				"input_tokens":  80,
				"output_tokens": 10,
				"total_tokens":  90,
			})
		default:
			// Translation attempt 2 succeeds.
			writeJSONResponseWithUsage(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": mustMarshalJSON(t, translationOutput{
					Dialogue: model.TranslateBatchOutput{{Index: 1, Text: []string{"走吧"}}},
				}),
			}, map[string]any{
				"input_tokens":  80,
				"output_tokens": 20,
				"total_tokens":  100,
			})
		}
	}, WithLogger(logger), WithMetrics(metrics))

	req := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 1, Speaker: "A", Text: []string{"秋葉原へ行こう"}}},
	}
	if _, err := p.RecognizeEntities(context.Background(), req.Transcript(), RecognizeEntitiesParams{
		Model:      "gpt-entity",
		MaxAttempt: 1,
	}); err != nil {
		t.Fatalf("RecognizeEntities failed: %v", err)
	}
	if _, err := p.Translate(context.Background(), req, TranslateParams{
		Model:      "gpt-translate",
		BatchSize:  1,
		MaxAttempt: 2,
	}); err != nil {
		t.Fatalf("Translate failed: %v", err)
	}

	if err := metrics.WriteTotal(); err != nil {
		t.Fatalf("WriteTotal failed: %v", err)
	}

	metricLines := strings.Split(strings.TrimSpace(metricBuf.String()), "\n")
	if len(metricLines) != 4 {
		t.Fatalf("expected 4 JSONL metric events (3 calls + total_usage), got %d:\n%s", len(metricLines), metricBuf.String())
	}

	wantTypes := []string{
		observation.MetricTypeEntityUsage,
		observation.MetricTypeTranslationBatchUsage,
		observation.MetricTypeTranslationBatchUsage,
		observation.MetricTypeTotalUsage,
	}
	for i, line := range metricLines {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("invalid metric JSONL line %d: %v", i, err)
		}
		if event["type"] != wantTypes[i] {
			t.Fatalf("metric line %d type = %v, want %q", i, event["type"], wantTypes[i])
		}
		if event["run_id"] != runID {
			t.Fatalf("metric line %d run_id = %v, want %q", i, event["run_id"], runID)
		}
	}

	report := metrics.Snapshot()
	if report.Requests != 3 || report.Errors != 1 || report.Retries != 1 {
		t.Fatalf("unexpected report counts: %#v", report)
	}
	if report.Usage.InputTokens != 210 ||
		report.Usage.CachedTokens != 10 ||
		report.Usage.OutputTokens != 45 ||
		report.Usage.ReasoningTokens != 5 ||
		report.Usage.TotalTokens != 255 {
		t.Fatalf("unexpected aggregated usage: %#v", report.Usage)
	}

	logLines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(logLines) != 3 {
		t.Fatalf("expected 3 JSON log lines (1 recognize ok, 1 retry warn, 1 translate ok), got %d:\n%s", len(logLines), logBuf.String())
	}
	for _, line := range logLines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON log line %q: %v", line, err)
		}
		if entry["run_id"] != runID {
			t.Fatalf("expected log run_id %q, got %v", runID, entry["run_id"])
		}
		if entry["time"] == "" || entry["time"] == nil {
			t.Fatalf("expected timestamp in log entry: %#v", entry)
		}
	}
}
