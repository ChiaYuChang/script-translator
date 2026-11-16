package prompt

import (
	"strings"
	"testing"

	"go-script-translate/internal/model"
)

func TestRenderDefaults(t *testing.T) {
	lang := DefaultLanguage()

	er, err := RenderEntityRecognition(lang, nil)
	if err != nil {
		t.Fatalf("RenderEntityRecognition default failed: %v", err)
	}
	for _, want := range []string{
		"You are an expert Japanese visual novel and game script entity recognition and localization assistant.",
		"propose appropriate Traditional Chinese translations for Taiwan (zh-TW).",
		"- 新堂ミナト: 新堂湊\n  - ミナト: 湊\n- 朝倉ユイ: 朝倉由依",
		"朝倉ユイ:ミナトちゃん、秋葉原へ行こう。\n新堂ミナト:いいね。",
		`"from": "ミナトちゃん"`,
		`"aliases": []`,
		"EXAMPLE EXPLANATION\n\n- \"新堂ミナト\" is already known",
	} {
		if !strings.Contains(er, want) {
			t.Fatalf("default entity recognition prompt missing %q:\n%s", want, er)
		}
	}

	tr, err := RenderTranslation(lang, nil)
	if err != nil {
		t.Fatalf("RenderTranslation default failed: %v", err)
	}
	for _, want := range []string{
		"You are an expert Japanese visual novel and game script localization translator working into Traditional Chinese for Taiwan (zh-TW).",
		"culturally appropriate Traditional Chinese (Taiwan (zh-TW))",
		"- 新堂ミナト: 新堂湊\n  - ミナト: 湊\n- 朝倉ユイ: 朝倉由依",
		"History:\n朝倉ユイ:「早安！今天天氣真好。」",
		`"name": ""`,
		`"text": "午後三時。放課後の教室には、二人だけが残っていた。"`,
		`"text": "「早安，由依。」"`,
		"The final dialogue unit demonstrates narration",
	} {
		if !strings.Contains(tr, want) {
			t.Fatalf("default translation prompt missing %q:\n%s", want, tr)
		}
	}
}

func TestRenderCustomLanguageAndExamples(t *testing.T) {
	lang := Language{
		From:     "Korean",
		To:       "English",
		Location: "United States",
	}

	erEx := EntityRecognitionExample{
		Input: EntityRecognitionExampleInput{
			KnownEntities: []RecognizedEntityExample{
				{Canonical: model.NamePair{From: "지수", To: "Jisoo"}},
			},
			Transcript: []ScriptLine{
				{Speaker: "지수", Text: "서울에 가자."},
			},
		},
		Output: EntityRecognitionExampleOutput{
			Entity: []RecognizedEntityExample{
				{Canonical: model.NamePair{From: "서울", To: "Seoul"}},
			},
		},
	}

	er, err := RenderEntityRecognition(lang, &erEx)
	if err != nil {
		t.Fatalf("RenderEntityRecognition custom failed: %v", err)
	}
	for _, want := range []string{
		"expert Korean visual novel and game script entity recognition",
		"appropriate English translations for United States.",
		"- 지수: Jisoo",
		"지수:서울에 가자.",
		`"to": "Seoul"`,
		`"aliases": []`,
	} {
		if !strings.Contains(er, want) {
			t.Fatalf("custom entity prompt missing %q:\n%s", want, er)
		}
	}
	if strings.Contains(er, "EXAMPLE EXPLANATION") || strings.Contains(er, "新堂ミナト") {
		t.Fatalf("unexpected default explanation/entities in custom prompt:\n%s", er)
	}

	trEx := TranslationExample{
		Input: TranslationExampleInput{
			Entries: []RecognizedEntityExample{
				{Canonical: model.NamePair{From: "지수", To: "Jisoo"}},
			},
			Dialogue: []TranslateUnitExample{
				{Index: 0, Speaker: "지수", Text: "안녕."},
			},
		},
		Output: TranslationExampleOutput{
			Dialogue: []model.TranslatedText{
				{Index: 0, Text: "Hey there."},
			},
		},
	}

	langNoLoc := Language{From: "Korean", To: "English"}
	tr, err := RenderTranslation(langNoLoc, &trEx)
	if err != nil {
		t.Fatalf("RenderTranslation custom failed: %v", err)
	}
	for _, want := range []string{
		"working into English.",
		"culturally appropriate English while preserving",
		"- 지수: Jisoo",
		`"text": "Hey there."`,
	} {
		if !strings.Contains(tr, want) {
			t.Fatalf("custom translation prompt missing %q:\n%s", want, tr)
		}
	}
	if strings.Contains(tr, "\nHistory:\n") || strings.Contains(tr, "Taiwan") || strings.Contains(tr, "新堂ミナト") {
		t.Fatalf("unexpected History/default locale/default example in custom prompt:\n%s", tr)
	}
}

func TestRenderRejectsInvalidExamples(t *testing.T) {
	_, err := RenderEntityRecognition(DefaultLanguage(), &EntityRecognitionExample{
		Input: EntityRecognitionExampleInput{},
	})
	if err == nil {
		t.Fatal("expected empty transcript to fail validation")
	}

	_, err = RenderTranslation(DefaultLanguage(), &TranslationExample{
		Input: TranslationExampleInput{
			Dialogue: []TranslateUnitExample{{Index: 0, Text: "a"}},
		},
		Output: TranslationExampleOutput{
			Dialogue: []model.TranslatedText{{Index: 1, Text: "b"}},
		},
	})
	if err == nil {
		t.Fatal("expected mismatched example index to fail validation")
	}
}

func TestRawTemplateAndDefaultExampleJSON(t *testing.T) {
	tmplER, err := RawTemplate(KindEntityRecognition)
	if err != nil || !strings.Contains(tmplER, "{{.Language.From}}") {
		t.Fatalf("unexpected RawTemplate(%q): (%v) %s", KindEntityRecognition, err, tmplER)
	}
	tmplTR, err := RawTemplate(KindTranslation)
	if err != nil || !strings.Contains(tmplTR, "{{.Language.TargetStyle}}") {
		t.Fatalf("unexpected RawTemplate(%q): (%v) %s", KindTranslation, err, tmplTR)
	}
	if _, err := RawTemplate("unknown"); err == nil {
		t.Fatal("expected unknown kind to fail RawTemplate")
	}

	jsonER, err := DefaultExampleJSON(KindEntityRecognition)
	if err != nil || !strings.Contains(jsonER, `"known_entities"`) || !strings.Contains(jsonER, "新堂ミナト") {
		t.Fatalf("unexpected DefaultExampleJSON(%q): (%v) %s", KindEntityRecognition, err, jsonER)
	}
	jsonTR, err := DefaultExampleJSON(KindTranslation)
	if err != nil || !strings.Contains(jsonTR, `"entries"`) || !strings.Contains(jsonTR, `"name": "新堂ミナト"`) {
		t.Fatalf("unexpected DefaultExampleJSON(%q): (%v) %s", KindTranslation, err, jsonTR)
	}
	if _, err := DefaultExampleJSON("unknown"); err == nil {
		t.Fatal("expected unknown kind to fail DefaultExampleJSON")
	}
}
