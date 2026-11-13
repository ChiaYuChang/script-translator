package llm

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"go-script-translate/internal/model"
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

func newTestProvider(t *testing.T, handler http.HandlerFunc) (*Provider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := openai.NewClient(
		option.WithBaseURL(srv.URL+"/v1"),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	return NewProvider(&client), srv
}

func writeJSONResponse(t *testing.T, w http.ResponseWriter, status string, contentItem map[string]any) {
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

func parseEntriesSection(t *testing.T, prompt string) ([]model.Entity, any) {
	t.Helper()
	const prefix = "Entries:\n"
	if !strings.HasPrefix(prompt, prefix) {
		t.Fatalf("prompt missing Entries header:\n%s", prompt)
	}
	rest := prompt[len(prefix):]
	end := strings.Index(rest, "\n\n")
	if end < 0 {
		t.Fatalf("prompt missing section separator after Entries:\n%s", prompt)
	}
	rawJSON := []byte(rest[:end])

	var rawAny any
	if err := json.Unmarshal(rawJSON, &rawAny); err != nil {
		t.Fatalf("unmarshal Entries section as JSON: %v", err)
	}
	var entities []model.Entity
	if err := json.Unmarshal(rawJSON, &entities); err != nil {
		t.Fatalf("unmarshal Entries section into []model.Entity: %v", err)
	}
	return entities, rawAny
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

	// All batches include the exact Entries glossary via JSON round-trip.
	for i, r := range requests {
		gotEntities, _ := parseEntriesSection(t, r.Input)
		if !reflect.DeepEqual(gotEntities, req.Entity) {
			t.Fatalf("request %d Entries mismatch:\ngot  %#v\nwant %#v", i+1, gotEntities, req.Entity)
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
	gotEntities, rawEntries := parseEntriesSection(t, requests[1].Input)
	arr, ok := rawEntries.([]any)
	if !ok || arr == nil || len(arr) != 0 || len(gotEntities) != 0 {
		t.Fatalf("expected non-null empty Entries JSON array [], got %#v", rawEntries)
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
