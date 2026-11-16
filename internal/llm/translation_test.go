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
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"

	"go-script-translate/internal/model"
	"go-script-translate/internal/observation"
	"go-script-translate/internal/prompt"
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

func newTestProvider(t *testing.T, handler http.HandlerFunc, opts ...ProviderOption) *Provider {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	client := openai.NewClient(
		option.WithBaseURL(srv.URL+"/v1"),
		option.WithAPIKey("test-key"),
		option.WithMaxRetries(0),
	)
	return NewProvider(&client, opts...)
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

func parseInputSection(t *testing.T, prompt string) []translateInputUnit {
	t.Helper()
	const marker = "\n\nInput:\n"
	idx := strings.Index(prompt, marker)
	if idx < 0 {
		t.Fatalf("prompt missing Input section:\n%s", prompt)
	}
	raw := prompt[idx+len(marker):]
	if strings.Contains(raw, "line_index") {
		t.Fatalf("prompt Input section unexpectedly leaked line_index:\n%s", raw)
	}
	var units []translateInputUnit
	if err := json.Unmarshal([]byte(raw), &units); err != nil {
		t.Fatalf("unmarshal Input section: %v", err)
	}
	return units
}

func TestTranslateValidationGuards(t *testing.T) {
	var calls atomic.Int32
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
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

func TestTranslateSingleBatchReorders(t *testing.T) {
	var captured capturedResponseRequest
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeCapturedRequest(t, r)
		// Return indices reversed.
		out := mustMarshalJSON(t, translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: 1, Text: "朝陽灑落，鳥兒正在鳴叫。"},
				{Index: 0, Text: "早安。"},
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
			{Index: 0, LineIndex: 12, Speaker: "新堂ミナト", Text: "おはよう。"},
			{Index: 1, LineIndex: 25, Speaker: "", Text: "朝の日差しが差す。鳥が鳴いている。"},
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
		{Index: 0, Text: "早安。"},
		{Index: 1, Text: "朝陽灑落，鳥兒正在鳴叫。"},
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
	wantWire := []translateInputUnit{
		{Index: 0, Speaker: "新堂ミナト", Text: "おはよう。"},
		{Index: 1, Speaker: "", Text: "朝の日差しが差す。鳥が鳴いている。"},
	}
	if units := parseInputSection(t, captured.Input); !reflect.DeepEqual(units, wantWire) {
		t.Fatalf("Input section mismatch:\ngot  %#v\nwant %#v", units, wantWire)
	}
}

func TestTranslateMultiBatchHistoryAndEntries(t *testing.T) {
	var requests []capturedResponseRequest
	batchResponses := []string{
		mustMarshalJSON(t, translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: 0, Text: "湊說早安"},
				{Index: 1, Text: "由依回早安"},
			},
		}),
		mustMarshalJSON(t, translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: 3, Text: "他們走出教室"},
				{Index: 2, Text: "鈴聲響起了"},
			},
		}),
		mustMarshalJSON(t, translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: 4, Text: "放學了。"},
			},
		}),
	}

	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
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
			{Index: 0, LineIndex: 10, Speaker: "新堂ミナト", Text: "おはよう"},
			{Index: 1, LineIndex: 20, Speaker: "朝倉ユイ", Text: "おはよう、ミナト"},
			{Index: 2, LineIndex: 30, Speaker: "", Text: "チャイムが鳴る。"},
			{Index: 3, LineIndex: 35, Speaker: "", Text: "二人は教室を出た。"},
			{Index: 4, LineIndex: 42, Speaker: "新堂ミナト", Text: "もう放課後か。"},
		},
	}

	got, err := p.Translate(context.Background(), req, defaultTestTranslateParams())
	if err != nil {
		t.Fatalf("Translate failed: %v", err)
	}

	want := model.TranslateResponse{
		{Index: 0, Text: "湊說早安"},
		{Index: 1, Text: "由依回早安"},
		{Index: 2, Text: "鈴聲響起了"},
		{Index: 3, Text: "他們走出教室"},
		{Index: 4, Text: "放學了。"},
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

	if units := parseInputSection(t, requests[2].Input); len(units) != 1 || units[0].Index != 4 {
		t.Fatalf("batch 3 Input mismatch: %#v", units)
	}
}

func TestTranslateNoHistoryPollutionAcrossCalls(t *testing.T) {
	var requests []capturedResponseRequest
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		req := decodeCapturedRequest(t, r)
		requests = append(requests, req)
		units := parseInputSection(t, req.Input)
		resp := translationOutput{
			Dialogue: model.TranslateBatchOutput{
				{Index: units[0].Index, Text: "已翻譯"},
			},
		}
		b, _ := json.Marshal(resp)
		writeJSONResponse(t, w, "completed", map[string]any{
			"type": "output_text",
			"text": string(b),
		})
	})

	req1 := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 1, Text: "一"}},
	}
	req2 := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 2, Text: "二"}},
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
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
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
				"text": `{"dialogue":[{"text":"錯"}]}`,
			})
		case 3:
			// Batch 1 attempt 3: valid.
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue":[{"index":0,"text":"第一句"}]}`,
			})
		case 4:
			// Batch 2 attempt 1: valid.
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue":[{"index":1,"text":"第二句"}]}`,
			})
		default:
			t.Fatalf("unexpected call %d", n)
		}
	})

	req := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{
			{Index: 0, Speaker: "A", Text: "一"},
			{Index: 1, Speaker: "B", Text: "二"},
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
		{Index: 0, Text: "第一句"},
		{Index: 1, Text: "第二句"},
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
					"text": `{"dialogue":[{"index":3,"text":"ok"}]}`,
				})
			},
		},
		{
			name: "duplicate index",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":"a"},{"index":3,"text":"b"}]}`,
				})
			},
		},
		{
			name: "unknown index",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":"a"},{"index":999,"text":"b"}]}`,
				})
			},
		},
		{
			name: "null text",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":null},{"index":4,"text":"b"}]}`,
				})
			},
		},
		{
			name: "empty text string",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":""},{"index":4,"text":"b"}]}`,
				})
			},
		},
		{
			name: "whitespace only text",
			resp: func(w http.ResponseWriter) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[{"index":3,"text":"  \t\n "},{"index":4,"text":"b"}]}`,
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
					"text": `{"dialogue":[{"index":3,"text":"a"},{"index":4,"text":"b"}]}`,
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
			p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if n == 1 {
					writeJSONResponse(t, w, "completed", map[string]any{
						"type": "output_text",
						"text": `{"dialogue":[{"index":1,"text":"一"},{"index":2,"text":"二"}]}`,
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
					{Index: 1, Text: "1"},
					{Index: 2, Text: "2"},
					{Index: 3, Text: "3"},
					{Index: 4, Text: "4"},
					{Index: 5, Text: "5"},
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

	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			cancel()
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"dialogue":[{"index":1,"text":"一"}]}`,
			})
			return
		}
		t.Fatal("server should not receive batch 2 after context cancellation")
	})

	req := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{
			{Index: 1, Text: "1"},
			{Index: 2, Text: "2"},
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
			body: `{"dialogue":[{"text":"早安"}]}`,
		},
		{
			name: "null index",
			body: `{"dialogue":[{"index":null,"text":"早安"}]}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": tc.body,
				})
			})

			req := model.TranslateRequest{
				Dialogue: []model.TranslateUnit{
					{Index: 0, Speaker: "新堂ミナト", Text: "おはよう"},
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

func TestTranslateRejectsMismatchedOuterDelimitersAndRetries(t *testing.T) {
	cases := []struct {
		name       string
		sourceText string
		badText    string
		goodText   string
		wantErrSub string
	}{
		{
			name:       "dropped dialogue quotes triggers retry",
			sourceText: "「おはよう」",
			badText:    "早安",
			goodText:   "「早安」",
			wantErrSub: "must preserve outer 「」 wrapper",
		},
		{
			name:       "dropped double corner quotes triggers retry",
			sourceText: "『おはよう』",
			badText:    "「早安」",
			goodText:   "『早安』",
			wantErrSub: "must preserve outer 『』 wrapper",
		},
		{
			name:       "half-width monologue parens rejected",
			sourceText: "（したいな……）",
			badText:    "(好想做啊……)",
			goodText:   "（好想做啊……）",
			wantErrSub: "must preserve outer （） wrapper",
		},
		{
			name:       "empty inner monologue rejected",
			sourceText: "（したいな……）",
			badText:    "（   ）",
			goodText:   "（好想做啊……）",
			wantErrSub: "empty content inside outer （）",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
				txt := tc.badText
				if calls.Add(1) > 1 {
					txt = tc.goodText
				}
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": mustMarshalJSON(t, translationOutput{
						Dialogue: model.TranslateBatchOutput{{Index: 0, Text: txt}},
					}),
				})
			})

			req := model.TranslateRequest{
				Dialogue: []model.TranslateUnit{
					{Index: 0, LineIndex: 1, Speaker: "俺", Text: tc.sourceText},
				},
			}

			// First verify MaxAttempt=1 surfaces the exact validation error.
			if _, err := p.Translate(context.Background(), req, TranslateParams{
				Model:      "gpt-test",
				BatchSize:  1,
				MaxAttempt: 1,
			}); err == nil || !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Fatalf("expected error containing %q, got %v", tc.wantErrSub, err)
			}

			// Reset and verify MaxAttempt=2 recovers on retry.
			calls.Store(0)
			got, err := p.Translate(context.Background(), req, TranslateParams{
				Model:      "gpt-test",
				BatchSize:  1,
				MaxAttempt: 2,
			})
			if err != nil {
				t.Fatalf("expected retry to recover, got %v", err)
			}
			if len(got) != 1 || got[0].Text != tc.goodText {
				t.Fatalf("unexpected retry output: %#v", got)
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
			{Index: 0, Text: "早安"},
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
		`{"dialogue":[{"text":"缺 index"}]}`,
		`{"dialogue":[{"index":null,"text":"null index"}]}`,
		`{"dialogue":[{"index":0,"text":null}]}`,
		`{"dialogue":[{"index":0,"text":["陣列應拒絕"]}]}`,
		`{"dialogue":[{"index":0,"text":"ok","extra":true}]}`,
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

	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
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
					Dialogue: model.TranslateBatchOutput{{Index: 1, Text: "走吧"}},
				}),
			}, map[string]any{
				"input_tokens":  80,
				"output_tokens": 20,
				"total_tokens":  100,
			})
		}
	}, WithLogger(logger), WithMetrics(metrics))

	req := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 1, Speaker: "A", Text: "秋葉原へ行こう"}},
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
		if event["type"] == observation.MetricTypeTranslationBatchUsage {
			if event["batch"] != float64(1) || event["total_batch"] != float64(1) {
				t.Fatalf("expected batch=1, total_batch=1 on translation metric line %d, got batch=%v total_batch=%v", i, event["batch"], event["total_batch"])
			}
		} else if _, hasTotalBatch := event["total_batch"]; hasTotalBatch {
			t.Fatalf("unexpected total_batch on non-batch metric line %d (%v)", i, event["type"])
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
	for i, line := range logLines {
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
		if i > 0 && (entry["batch"] != float64(1) || entry["total_batch"] != float64(1)) {
			t.Fatalf("expected batch=1 and total_batch=1 on translation log line %d, got: %#v", i, entry)
		}
	}
}

func writeBatchResponse(t *testing.T, w http.ResponseWriter, items []model.TranslatedText) {
	t.Helper()
	out := mustMarshalJSON(t, translationOutput{
		Dialogue: model.TranslateBatchOutput(items),
	})
	writeJSONResponse(t, w, "completed", map[string]any{
		"type": "output_text",
		"text": out,
	})
}

func TestTranslateWaitTimeAnd429ExponentialBackoff(t *testing.T) {
	var calls atomic.Int32
	var slept []time.Duration

	recResp, err := json.Marshal(recognitionOutput{
		Entity: []recognizedEntity{
			{Canonical: model.NamePair{From: "ミナト", To: "湊"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	p := newTestProvider(
		t,
		func(w http.ResponseWriter, _ *http.Request) {
			n := calls.Add(1)
			switch n {
			case 1:
				// RecognizeEntities call: first LLM call, no pre-wait.
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": string(recResp),
				})
			case 2, 3:
				// Batch 1 attempts 1 & 2 -> HTTP 429 triggers exponential backoff (3s, 6s).
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit","type":"rate_limit_error"}}`))
			case 4:
				// Batch 1 attempt 3 -> invalid schema triggers WaitTime (1s).
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"dialogue":[]}`,
				})
			case 5:
				// Batch 1 attempt 4 -> succeeds.
				writeBatchResponse(t, w, []model.TranslatedText{{Index: 0, Text: "「早安」"}})
			default:
				// Batch 2 attempt 1 -> after WaitTime (1s), succeeds.
				writeBatchResponse(t, w, []model.TranslatedText{{Index: 1, Text: "「晚安」"}})
			}
		},
		WithWaitTime(1*time.Second),
		WithRetryTime(3*time.Second),
		withSleep(func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}),
	)

	dialogue := []model.TranslateUnit{
		{Index: 0, Speaker: "ミナト", Text: "「おはよう」"},
		{Index: 1, Speaker: "ミナト", Text: "「おやすみ」"},
	}
	if _, err := p.RecognizeEntities(context.Background(), model.TranslateRequest{Dialogue: dialogue}.Transcript(), RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		MaxAttempt: 2,
	}); err != nil {
		t.Fatalf("RecognizeEntities failed: %v", err)
	}

	got, err := p.Translate(context.Background(), model.TranslateRequest{Dialogue: dialogue}, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  1,
		MaxAttempt: 4,
	})
	if err != nil {
		t.Fatalf("Translate failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 translated units, got %d", len(got))
	}

	wantSlept := []time.Duration{
		1 * time.Second, // WaitTime before Batch 1 (follows RecognizeEntities)
		3 * time.Second, // 429 retry #1: 3s * 1<<0
		6 * time.Second, // 429 retry #2: 3s * 1<<1
		1 * time.Second, // non-429 retry: WaitTime
		1 * time.Second, // WaitTime before Batch 2
	}
	if !reflect.DeepEqual(slept, wantSlept) {
		t.Fatalf("recorded sleeps = %v, want %v", slept, wantSlept)
	}
}

func TestTranslate5xxExponentialBackoffAndZeroNoWait(t *testing.T) {
	var calls atomic.Int32
	var slept []time.Duration

	p := newTestProvider(
		t,
		func(w http.ResponseWriter, _ *http.Request) {
			switch calls.Add(1) {
			case 1, 2:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"error":{"message":"upstream busy","type":"server_error"}}`))
			default:
				writeBatchResponse(t, w, []model.TranslatedText{{Index: 0, Text: "「早安」"}})
			}
		},
		WithWaitTime(0),
		WithRetryTime(4*time.Second),
		withSleep(func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}),
	)

	got, err := p.Translate(context.Background(), model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 0, Text: "「おはよう」"}},
	}, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  1,
		MaxAttempt: 3,
	})
	if err != nil {
		t.Fatalf("Translate failed: %v", err)
	}
	if len(got) != 1 || got[0].Text != "「早安」" {
		t.Fatalf("unexpected translation: %#v", got)
	}

	wantSlept := []time.Duration{4 * time.Second, 8 * time.Second}
	if !reflect.DeepEqual(slept, wantSlept) {
		t.Fatalf("recorded sleeps = %v, want %v", slept, wantSlept)
	}

	// RetryTime == 0 disables backoff sleep even on 429/5xx.
	var zeroSlept []time.Duration
	var zeroCalls atomic.Int32
	pZero := newTestProvider(
		t,
		func(w http.ResponseWriter, _ *http.Request) {
			if zeroCalls.Add(1) == 1 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit","type":"rate_limit_error"}}`))
				return
			}
			writeBatchResponse(t, w, []model.TranslatedText{{Index: 0, Text: "「早安」"}})
		},
		WithWaitTime(0),
		WithRetryTime(0),
		withSleep(func(_ context.Context, d time.Duration) error {
			zeroSlept = append(zeroSlept, d)
			return nil
		}),
	)
	if _, err := pZero.Translate(context.Background(), model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 0, Text: "「おはよう」"}},
	}, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  1,
		MaxAttempt: 2,
	}); err != nil {
		t.Fatalf("Translate with zero timers failed: %v", err)
	}
	if len(zeroSlept) != 0 {
		t.Fatalf("expected zero sleeps when WaitTime=0 and RetryTime=0, got %v", zeroSlept)
	}
}

func TestTranslateResumeFromAndPartialCheckpoint(t *testing.T) {
	var seenInputs []string
	var snapshots [][]model.TranslatedText

	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var captured capturedResponseRequest
		if err := json.Unmarshal(raw, &captured); err != nil {
			t.Fatal(err)
		}
		seenInputs = append(seenInputs, captured.Input)

		switch len(seenInputs) {
		case 1:
			writeBatchResponse(t, w, []model.TranslatedText{{Index: 2, Text: "（第三句）"}})
		case 2:
			writeBatchResponse(t, w, []model.TranslatedText{{Index: 3, Text: "第四句。"}})
		default:
			t.Fatalf("unexpected extra request #%d", len(seenInputs))
		}
	})

	req := model.TranslateRequest{
		Entity: []model.Entity{{Canonical: model.NamePair{From: "ミナト", To: "湊"}}},
		Dialogue: []model.TranslateUnit{
			{Index: 0, Speaker: "ミナト", Text: "「一」"},
			{Index: 1, Speaker: "ミナト", Text: "「二」"},
			{Index: 2, Speaker: "ミナト", Text: "（三）"},
			{Index: 3, Text: "四。"},
		},
	}

	got, err := p.Translate(context.Background(), req, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  1,
		MaxAttempt: 2,
		ResumeFrom: []model.TranslatedText{
			{Index: 0, Text: "「第一句」"},
			{Index: 1, Text: "「第二句」"},
		},
		OnBatchComplete: func(completed []model.TranslatedText) error {
			snapshots = append(snapshots, slices.Clone(completed))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Translate resume failed: %v", err)
	}

	if len(seenInputs) != 2 {
		t.Fatalf("expected 2 requests for remaining units, got %d", len(seenInputs))
	}
	// First remaining batch should already include History with resumed units 0 and 1.
	if !strings.Contains(seenInputs[0], "ミナト:「第一句」") || !strings.Contains(seenInputs[0], "ミナト:「第二句」") {
		t.Fatalf("expected resumed history in first batch prompt, got:\n%s", seenInputs[0])
	}
	if len(snapshots) != 2 || len(snapshots[0]) != 3 || len(snapshots[1]) != 4 {
		t.Fatalf("unexpected checkpoint snapshots: %#v", snapshots)
	}
	want := model.TranslateResponse{
		{Index: 0, Text: "「第一句」"},
		{Index: 1, Text: "「第二句」"},
		{Index: 2, Text: "（第三句）"},
		{Index: 3, Text: "第四句。"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("translated mismatch:\ngot  %#v\nwant %#v", got, want)
	}

	// Invalid ResumeFrom index mismatch returns error without calling API.
	if _, err := p.Translate(context.Background(), req, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  1,
		MaxAttempt: 1,
		ResumeFrom: []model.TranslatedText{{Index: 9, Text: "「壞掉」"}},
	}); err == nil {
		t.Fatal("expected error for mismatched ResumeFrom index")
	}
}

func TestTranslateUnexpectedEOFShrinksBatchSizeAndUsesBackoff(t *testing.T) {
	var seenBatchSizes []int
	var slept []time.Duration
	var checkpoints [][]model.TranslatedText
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	p := newTestProvider(
		t,
		func(w http.ResponseWriter, r *http.Request) {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatal(err)
			}
			var req capturedResponseRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			_, inputJSON, ok := strings.Cut(req.Input, "\nInput:\n")
			if !ok {
				t.Fatalf("missing Input section:\n%s", req.Input)
			}
			var units []translateInputUnit
			if err := json.Unmarshal([]byte(inputJSON), &units); err != nil {
				t.Fatal(err)
			}
			seenBatchSizes = append(seenBatchSizes, len(units))

			if len(seenBatchSizes) == 1 {
				// First attempt sends 10 units; simulate abrupt upstream disconnect (unexpected EOF).
				hj, ok := w.(http.Hijacker)
				if !ok {
					t.Fatal("ResponseWriter does not implement http.Hijacker")
				}
				conn, _, err := hj.Hijack()
				if err != nil {
					t.Fatal(err)
				}
				_, _ = conn.Write([]byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{\"id\":\"partial"))
				_ = conn.Close()
				return
			}

			out := make([]model.TranslatedText, len(units))
			for i, u := range units {
				out[i] = model.TranslatedText{
					Index: u.Index,
					Text:  "「譯文」",
				}
			}
			writeBatchResponse(t, w, out)
		},
		WithLogger(logger),
		WithWaitTime(1*time.Second),
		WithRetryTime(3*time.Second),
		withSleep(func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}),
	)

	dialogue := make([]model.TranslateUnit, 10)
	for i := range dialogue {
		dialogue[i] = model.TranslateUnit{Index: i, Speaker: "ミナト", Text: "「台詞」"}
	}

	got, err := p.Translate(context.Background(), model.TranslateRequest{Dialogue: dialogue}, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  10,
		MaxAttempt: 3,
		OnBatchComplete: func(completed []model.TranslatedText) error {
			checkpoints = append(checkpoints, slices.Clone(completed))
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Translate failed: %v", err)
	}
	if len(got) != 10 {
		t.Fatalf("expected 10 translated units, got %d", len(got))
	}
	for i, item := range got {
		if item.Index != i {
			t.Fatalf("unit %d has index %d", i, item.Index)
		}
	}

	// Attempt 1 tries 10 units (total_batch=1) -> unexpected EOF -> shrinks 10*9/10=9
	// (recalculates total_batch=2) -> batch 1 completes 9 units -> batch 2 finishes remaining 1.
	wantSizes := []int{10, 9, 1}
	if !reflect.DeepEqual(seenBatchSizes, wantSizes) {
		t.Fatalf("seen batch sizes = %v, want %v", seenBatchSizes, wantSizes)
	}
	wantSlept := []time.Duration{3 * time.Second, 1 * time.Second}
	if !reflect.DeepEqual(slept, wantSlept) {
		t.Fatalf("recorded sleeps = %v, want %v", slept, wantSlept)
	}
	if len(checkpoints) != 2 || len(checkpoints[0]) != 9 || len(checkpoints[1]) != 10 {
		t.Fatalf("unexpected checkpoints: %#v", checkpoints)
	}

	logLines := strings.Split(strings.TrimSpace(logBuf.String()), "\n")
	if len(logLines) != 3 {
		t.Fatalf("expected 3 JSON log lines (warn + 2 completions), got %d:\n%s", len(logLines), logBuf.String())
	}
	var entries []map[string]any
	for i, line := range logLines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("invalid JSON log line %d (%q): %v", i, line, err)
		}
		entries = append(entries, entry)
	}
	if entries[0]["msg"] != "translation batch attempt failed" ||
		entries[0]["batch"] != float64(1) ||
		entries[0]["total_batch"] != float64(1) ||
		entries[0]["batch_size"] != float64(10) ||
		entries[0]["next_batch_size"] != float64(9) ||
		entries[0]["next_total_batch"] != float64(2) ||
		entries[0]["backoff"] != true {
		t.Fatalf("unexpected warn log entry: %#v", entries[0])
	}
	if entries[1]["msg"] != "llm request completed" ||
		entries[1]["batch"] != float64(1) ||
		entries[1]["total_batch"] != float64(2) ||
		entries[1]["batch_size"] != float64(9) {
		t.Fatalf("unexpected batch 1 completion log entry: %#v", entries[1])
	}
	if entries[2]["msg"] != "llm request completed" ||
		entries[2]["batch"] != float64(2) ||
		entries[2]["total_batch"] != float64(2) ||
		entries[2]["batch_size"] != float64(9) {
		t.Fatalf("unexpected batch 2 completion log entry: %#v", entries[2])
	}
}

func TestTranslateCustomLanguageAndExample(t *testing.T) {
	var capturedInstructions string
	var calls atomic.Int32

	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var req struct {
			Instructions string `json:"instructions"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		capturedInstructions = req.Instructions
		writeJSONResponse(t, w, "completed", map[string]any{
			"type": "output_text",
			"text": `{"dialogue":[{"index":0,"text":"「Hi.」"}]}`,
		})
	})

	_, err := p.Translate(context.Background(), model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 0, Speaker: "지수", Text: "「안녕.」"}},
	}, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  5,
		MaxAttempt: 1,
		Language: prompt.Language{
			From: "Korean",
			To:   "English",
		},
		Example: &prompt.TranslationExample{
			Input: prompt.TranslationExampleInput{
				Dialogue: []prompt.TranslateUnitExample{
					{Index: 0, Speaker: "지수", Text: "「안녕.」"},
				},
			},
			Output: prompt.TranslationExampleOutput{
				Dialogue: []model.TranslatedText{
					{Index: 0, Text: "「Hello.」"},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Translate custom failed: %v", err)
	}
	if !strings.Contains(capturedInstructions, "working into English.") ||
		!strings.Contains(capturedInstructions, "original Korean dialogue") ||
		!strings.Contains(capturedInstructions, `"text": "「Hello.」"`) ||
		strings.Contains(capturedInstructions, "Taiwan") ||
		strings.Contains(capturedInstructions, "新堂ミナト") {
		t.Fatalf("unexpected custom translation instructions:\n%s", capturedInstructions)
	}

	beforeCalls := calls.Load()
	_, err = p.Translate(context.Background(), model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 0, Text: "「a」"}},
	}, TranslateParams{
		Model:      "translate-model-v1",
		BatchSize:  5,
		MaxAttempt: 1,
		Example:    &prompt.TranslationExample{},
	})
	if err == nil {
		t.Fatal("expected invalid translation example to fail")
	}
	if calls.Load() != beforeCalls {
		t.Fatalf("expected no HTTP calls on invalid example, got %d new calls", calls.Load()-beforeCalls)
	}
}
