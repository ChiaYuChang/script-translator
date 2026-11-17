package llm

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go-script-translate/internal/model"
	"go-script-translate/internal/prompt"
)

func TestMatchKnownEntities(t *testing.T) {
	dictionary := []model.Entity{
		{ID: 1, Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"}, Aliases: []model.NamePair{{From: "ミナト", To: "湊"}}},
		{ID: 2, Canonical: model.NamePair{From: "朝倉ユイ", To: "朝倉由依"}},
		{ID: 3, Canonical: model.NamePair{From: "", To: ""}},
		{ID: 4, Canonical: model.NamePair{From: "東京", To: "東京"}},
	}
	got := MatchKnownEntities("朝倉ユイ:ミナトは元気？", dictionary)
	if len(got) != 2 || got[0].ID != 1 || got[1].ID != 2 {
		t.Fatalf("unexpected matches: %#v", got)
	}
	if n := len(MatchKnownEntities("", dictionary)); n != 0 {
		t.Fatalf("matched %d entities in empty script", n)
	}
}

func TestMergeRecognizedEntities(t *testing.T) {
	known := model.Entity{
		ID:        1,
		Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
		Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
	}
	dictionary := []model.Entity{known}
	script := "新堂ミナト:ミナトちゃんは秋葉原に行きたい。"
	proposals := []recognizedEntity{
		{
			Canonical: model.NamePair{From: "新堂ミナト", To: "錯誤的覆寫"},
			Aliases: []model.NamePair{
				{From: "ミナト", To: "錯誤的覆寫"},
				{From: "ミナトちゃん", To: "小湊"},
				{From: "ミナトちゃん", To: "重複別名"},
			},
		},
		{Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"}, Aliases: []model.NamePair{}},
		{Canonical: model.NamePair{From: "不存在的地名", To: "虛構地點"}},
		{Canonical: model.NamePair{From: "ミナト", To: "錯誤的獨立人物"}},
	}
	got := mergeRecognizedEntities(script, []model.Entity{known}, dictionary, proposals)
	want := []model.Entity{
		{ID: 1, Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"}, Aliases: []model.NamePair{{From: "ミナト", To: "湊"}, {From: "ミナトちゃん", To: "小湊"}}},
		{Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merge mismatch:\ngot  %#v\nwant %#v", got, want)
	}
	if dictionary[0].Aliases[0].To != "湊" || len(dictionary[0].Aliases) != 1 {
		t.Fatal("merge modified the original dictionary")
	}
}

func TestMergeUnmatchedKnownEntityAlias(t *testing.T) {
	dictionary := []model.Entity{{ID: 2, Canonical: model.NamePair{From: "朝倉ユイ", To: "朝倉由依"}}}
	got := mergeRecognizedEntities("ユイちゃん:おはよう", nil, dictionary, []recognizedEntity{{
		Canonical: model.NamePair{From: "朝倉ユイ", To: "不得覆寫"},
		Aliases:   []model.NamePair{{From: "ユイちゃん", To: "小由依"}},
	}})
	if len(got) != 1 || got[0].ID != 2 || got[0].Canonical.To != "朝倉由依" || len(got[0].Aliases) != 1 {
		t.Fatalf("unexpected output: %#v", got)
	}
}

func TestNewAliasDoesNotStealExistingName(t *testing.T) {
	dictionary := []model.Entity{
		{ID: 1, Canonical: model.NamePair{From: "佐藤", To: "佐藤"}},
		{ID: 2, Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"}},
	}
	script := "新堂ミナト:佐藤に会う"
	known := MatchKnownEntities(script, dictionary)
	got := mergeRecognizedEntities(script, known, dictionary, []recognizedEntity{{
		Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
		Aliases:   []model.NamePair{{From: "佐藤", To: "別名錯誤"}},
	}})
	if len(got) != 2 || len(got[1].Aliases) != 0 {
		t.Fatalf("unexpected mapping: %#v", got)
	}
}

func TestRecognitionSchema(t *testing.T) {
	if _, err := ToSchemaMap(nil); err == nil {
		t.Fatal("expected ToSchemaMap(nil) to return error")
	}
	schemaMap, err := ToSchemaMap(recognitionSchema)
	if err != nil {
		t.Fatalf("ToSchemaMap(recognitionSchema): %v", err)
	}
	if len(schemaMap) == 0 {
		t.Fatal("expected non-empty recognition schema map")
	}

	resolved, err := recognitionSchema.Resolve(nil)
	if err != nil {
		t.Fatalf("resolve recognition schema: %v", err)
	}

	validBytes, err := json.Marshal(recognitionOutput{
		Entity: []recognizedEntity{
			{
				Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"},
				Aliases:   []model.NamePair{},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateJSON(resolved, string(validBytes)); err != nil {
		t.Fatalf("expected valid recognition payload to pass schema: %v", err)
	}

	for _, invalid := range []string{
		`{}`,
		`{"entity":null}`,
		`{"entity":[{"canonical":{"from":"A","to":"甲"}}]}`,
		`{"entity":[{"canonical":{"from":"A","to":"甲"},"aliases":null}]}`,
		`{"entity":[{"canonical":{"from":"A","to":"甲","extra":1},"aliases":[]}]}`,
		`{"entity":[{"canonical":{"from":"A"},"aliases":[]}]}`,
	} {
		if err := ValidateJSON(resolved, invalid); err == nil {
			t.Errorf("expected invalid recognition payload to fail schema: %s", invalid)
		}
	}
}

func TestRecognizeEntitiesUsesPassedModel(t *testing.T) {
	var captured capturedResponseRequest
	mockResp, err := json.Marshal(recognitionOutput{
		Entity: []recognizedEntity{
			{
				Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"},
				Aliases:   []model.NamePair{},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		captured = decodeCapturedRequest(t, r)
		writeJSONResponse(t, w, "completed", map[string]any{
			"type": "output_text",
			"text": string(mockResp),
		})
	})

	transcript := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{
			{Index: 0, Speaker: "新堂ミナト", Text: "秋葉原へ行こう。"},
		},
	}.Transcript()
	dictionary := []model.Entity{
		{
			ID:        1,
			Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
			Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
		},
	}

	if _, err := p.RecognizeEntities(context.Background(), transcript, RecognizeEntitiesParams{
		Model:      " ",
		Dictionary: dictionary,
		MaxAttempt: 1,
	}); err == nil {
		t.Fatal("expected error for empty model name")
	}
	if _, err := p.RecognizeEntities(context.Background(), transcript, RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		Dictionary: dictionary,
		MaxAttempt: 0,
	}); err == nil {
		t.Fatal("expected error for non-positive max attempt")
	}
	if _, err := p.RecognizeEntities(context.Background(), " \n ", RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		Dictionary: dictionary,
		MaxAttempt: 1,
	}); err == nil {
		t.Fatal("expected error for empty transcript")
	}

	got, err := p.RecognizeEntities(context.Background(), transcript, RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		Dictionary: dictionary,
		MaxAttempt: 1,
	})
	if err != nil {
		t.Fatalf("RecognizeEntities failed: %v", err)
	}
	if captured.Model != "entity-model-v1" {
		t.Fatalf("expected model entity-model-v1, got %q", captured.Model)
	}
	wantInput := "KnownEntities:\n- 新堂ミナト: 新堂湊\n  - ミナト: 湊\n\nTranscript:\n新堂ミナト:秋葉原へ行こう。"
	if captured.Input != wantInput {
		t.Fatalf("unexpected request input:\ngot:\n%s\nwant:\n%s", captured.Input, wantInput)
	}
	if len(got) != 2 || got[0].Canonical.To != "新堂湊" || got[1].Canonical.To != "秋葉原" {
		t.Fatalf("unexpected recognized entities: %#v", got)
	}

	// Empty KnownEntities section still renders cleanly before Transcript:.
	if _, err := p.RecognizeEntities(context.Background(), transcript, RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		MaxAttempt: 1,
	}); err != nil {
		t.Fatalf("RecognizeEntities without dictionary failed: %v", err)
	}
	wantEmptyKnownInput := "KnownEntities:\n\nTranscript:\n新堂ミナト:秋葉原へ行こう。"
	if captured.Input != wantEmptyKnownInput {
		t.Fatalf("unexpected empty KnownEntities input:\ngot:\n%s\nwant:\n%s", captured.Input, wantEmptyKnownInput)
	}
}

func TestRecognizeEntitiesRejectsInvalidSchemaOutput(t *testing.T) {
	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSONResponse(t, w, "completed", map[string]any{
			"type": "output_text",
			"text": `{"entity":[{"canonical":{"from":"秋葉原","to":"秋葉原"},"aliases":null}]}`,
		})
	})

	transcript := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 0, Speaker: "A", Text: "秋葉原"}},
	}.Transcript()
	if _, err := p.RecognizeEntities(context.Background(), transcript, RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		MaxAttempt: 1,
	}); err == nil {
		t.Fatal("expected schema validation error for null aliases")
	}
}

func TestRecognizeEntitiesRetriesWithinMaxAttemptAndSucceeds(t *testing.T) {
	var calls atomic.Int32
	mockResp, err := json.Marshal(recognitionOutput{
		Entity: []recognizedEntity{
			{
				Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"},
				Aliases:   []model.NamePair{},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"entity": [`,
			})
		case 2:
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": `{"entity":[{"canonical":{"from":"秋葉原","to":"秋葉原"},"aliases":null}]}`,
			})
		default:
			writeJSONResponse(t, w, "completed", map[string]any{
				"type": "output_text",
				"text": string(mockResp),
			})
		}
	})

	transcript := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 0, Speaker: "A", Text: "秋葉原"}},
	}.Transcript()
	got, err := p.RecognizeEntities(context.Background(), transcript, RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		MaxAttempt: 3,
	})
	if err != nil {
		t.Fatalf("expected retry to succeed, got: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("expected 3 attempts, got %d", calls.Load())
	}
	if len(got) != 1 || got[0].Canonical.From != "秋葉原" {
		t.Fatalf("unexpected recognized entities: %#v", got)
	}
}

func TestRecognizeEntitiesWaitAndRetryBackoff(t *testing.T) {
	var calls atomic.Int32
	mockResp, err := json.Marshal(recognitionOutput{
		Entity: []recognizedEntity{
			{Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"}, Aliases: []model.NamePair{}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var slept []time.Duration
	p := newTestProvider(
		t,
		func(w http.ResponseWriter, _ *http.Request) {
			switch calls.Add(1) {
			case 1, 2:
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"error":{"message":"rate limit","type":"rate_limit_error"}}`))
			case 3:
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": `{"entity": [`,
				})
			default:
				writeJSONResponse(t, w, "completed", map[string]any{
					"type": "output_text",
					"text": string(mockResp),
				})
			}
		},
		WithWaitTime(2*time.Second),
		WithRetryTime(5*time.Second),
		withSleep(func(_ context.Context, d time.Duration) error {
			slept = append(slept, d)
			return nil
		}),
	)

	transcript := model.TranslateRequest{
		Dialogue: []model.TranslateUnit{{Index: 0, Speaker: "A", Text: "秋葉原"}},
	}.Transcript()
	got, err := p.RecognizeEntities(context.Background(), transcript, RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		MaxAttempt: 4,
	})
	if err != nil {
		t.Fatalf("RecognizeEntities failed: %v", err)
	}
	if len(got) != 1 || got[0].Canonical.From != "秋葉原" {
		t.Fatalf("unexpected recognized entities: %#v", got)
	}
	// Two 429 retries (5s * 2^0, 5s * 2^1) followed by one non-429 retry wait (2s).
	wantSlept := []time.Duration{5 * time.Second, 10 * time.Second, 2 * time.Second}
	if !reflect.DeepEqual(slept, wantSlept) {
		t.Fatalf("recorded sleeps = %v, want %v", slept, wantSlept)
	}
}

func TestRecognizeEntitiesCustomLanguageAndExample(t *testing.T) {
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
			"text": `{"entity":[{"canonical":{"from":"Seoul","to":"Seoul"},"aliases":[]}]}`,
		})
	})

	_, err := p.RecognizeEntities(context.Background(), "A:Seoul", RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		MaxAttempt: 1,
		Language: prompt.Language{
			From:     "Korean",
			To:       "English",
			Location: "United States",
		},
		Example: &prompt.EntityRecognitionExample{
			Input: prompt.EntityRecognitionExampleInput{
				Transcript: []prompt.ScriptLine{
					{Speaker: "지수", Text: "서울에 가자."},
				},
			},
			Output: prompt.EntityRecognitionExampleOutput{
				Entity: []prompt.RecognizedEntityExample{
					{Canonical: model.NamePair{From: "서울", To: "Seoul"}},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("RecognizeEntities custom failed: %v", err)
	}
	if !strings.Contains(capturedInstructions, "expert Korean visual novel") ||
		!strings.Contains(capturedInstructions, "English translations for United States") ||
		!strings.Contains(capturedInstructions, "지수:서울에 가자.") ||
		strings.Contains(capturedInstructions, "Taiwan") {
		t.Fatalf("unexpected custom instructions:\n%s", capturedInstructions)
	}

	// Invalid example must fail before issuing HTTP request.
	beforeCalls := calls.Load()
	_, err = p.RecognizeEntities(context.Background(), "A:hello", RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		MaxAttempt: 1,
		Example:    &prompt.EntityRecognitionExample{},
	})
	if err == nil {
		t.Fatal("expected invalid example to fail")
	}
	if calls.Load() != beforeCalls {
		t.Fatalf("expected no HTTP calls on invalid example, got %d new calls", calls.Load()-beforeCalls)
	}
}

func TestRecognizeEntitiesChatCompletionsEndpoint(t *testing.T) {
	var seenPath string
	var seenRole0, seenRole1, seenFormatType string
	var seenStrict bool

	p := newTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		var req struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
			ResponseFormat struct {
				Type       string `json:"type"`
				JSONSchema struct {
					Name   string `json:"name"`
					Strict bool   `json:"strict"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatal(err)
		}
		if len(req.Messages) == 2 {
			seenRole0 = req.Messages[0].Role
			seenRole1 = req.Messages[1].Role
		}
		seenFormatType = req.ResponseFormat.Type
		seenStrict = req.ResponseFormat.JSONSchema.Strict

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl_er",
			"object": "chat.completion",
			"created": 1,
			"model": "entity-model-v1",
			"choices": [{
				"index": 0,
				"finish_reason": "stop",
				"message": {
					"role": "assistant",
					"content": "{\"entity\":[{\"canonical\":{\"from\":\"秋葉原\",\"to\":\"秋葉原\"},\"aliases\":[]}]}"
				}
			}],
			"usage": {"prompt_tokens": 10, "completion_tokens": 5, "total_tokens": 15}
		}`))
	}, WithEndpoint(EndpointChatCompletions))

	got, err := p.RecognizeEntities(context.Background(), "朝倉ユイ:秋葉原へ行こう。", RecognizeEntitiesParams{
		Model:      "entity-model-v1",
		MaxAttempt: 1,
	})
	if err != nil {
		t.Fatalf("RecognizeEntities chat_completions failed: %v", err)
	}
	if seenPath != "/v1/chat/completions" {
		t.Fatalf("HTTP path = %q, want /v1/chat/completions", seenPath)
	}
	if seenRole0 != "system" || seenRole1 != "user" || seenFormatType != "json_schema" || !seenStrict {
		t.Fatalf("unexpected chat completions payload: roles=(%q,%q) format=%q strict=%v", seenRole0, seenRole1, seenFormatType, seenStrict)
	}
	if len(got) != 1 || got[0].Canonical.From != "秋葉原" {
		t.Fatalf("unexpected recognized entities: %#v", got)
	}
}
