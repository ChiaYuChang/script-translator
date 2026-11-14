package translate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go-script-translate/internal/model"
)

func TestAssembleEndToEndWithExtract(t *testing.T) {
	const rawScript = `//【背景：DAILY_001】
<BG>nc_cafe_morning,IN,1000
<NAME_PLATE>新堂ミナト
「おはよう。
 今日もいい天気だね。」
「コーヒー、もう注文した？」
<PAUSE>

<NAME_PLATE>
 朝のカフェに、
 静かな音楽が流れていた。
<PAUSE>
<NAME_PLATE>ミナト
「あ、店員さんが来たよ。」
（財布、持ってたよな……）
<NAME_PLATE>未知キャラ
「いらっしゃいませ。」
<ACTOR>minato,SMILE
<SCENARIO_END>
`
	path := filepath.Join(t.TempDir(), "input.txt")
	if err := os.WriteFile(path, []byte(rawScript), 0o600); err != nil {
		t.Fatal(err)
	}
	script, err := Read(path)
	if err != nil {
		t.Fatalf("Read() failed: %v", err)
	}
	source, retained, err := Extract(script)
	if err != nil {
		t.Fatalf("Extract() failed: %v", err)
	}

	entities := []model.Entity{
		{
			ID:        1,
			Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
			Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
		},
	}

	// Provide translations out of order (including two utterances sharing LineIndex=2,
	// plus spoken 「」 + inner monologue （） sharing a NAME_PLATE).
	translated := model.TranslateResponse{
		{Index: source[5].Index, Text: "「歡迎光臨。」"},
		{Index: source[2].Index, Text: "早上的咖啡廳裡，流淌著輕柔的音樂。"},
		{Index: source[0].Index, Text: "「早安，今天天氣真好呢。」"},
		{Index: source[4].Index, Text: "（我應該有帶錢包吧……）"},
		{Index: source[3].Index, Text: "「啊，店員來了。」"},
		{Index: source[1].Index, Text: "「你已經點好咖啡了嗎？」"},
	}

	got, err := Assemble(source, translated, retained, entities)
	if err != nil {
		t.Fatalf("Assemble() failed: %v", err)
	}

	const want = `//【背景：DAILY_001】
<BG>nc_cafe_morning,IN,1000
<NAME_PLATE>新堂湊
「早安，今天天氣真好呢。」
「你已經點好咖啡了嗎？」
<PAUSE>

<NAME_PLATE>
早上的咖啡廳裡，流淌著輕柔的音樂。
<PAUSE>
<NAME_PLATE>湊
「啊，店員來了。」
（我應該有帶錢包吧……）
<NAME_PLATE>未知キャラ
「歡迎光臨。」
<ACTOR>minato,SMILE
<SCENARIO_END>
`
	if got != want {
		t.Fatalf("Assemble() mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAssembleSpeakerUsesExactMatchNotSubstring(t *testing.T) {
	source := []model.TranslateUnit{
		{Index: 0, LineIndex: 0, Speaker: "ミナト先輩", Text: "「やあ。」"},
		{Index: 1, LineIndex: 5, Speaker: "ミナト", Text: "「はい！」"},
	}
	translated := model.TranslateResponse{
		{Index: 0, Text: "「嗨。」"},
		{Index: 1, Text: "「是！」"},
	}
	entities := []model.Entity{
		{
			Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
			Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
		},
	}

	got, err := Assemble(source, translated, nil, entities)
	if err != nil {
		t.Fatalf("Assemble() failed: %v", err)
	}
	want := "<NAME_PLATE>ミナト先輩\n「嗨。」\n<NAME_PLATE>湊\n「是！」\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAssembleCanonicalPrecedenceOverAlias(t *testing.T) {
	source := []model.TranslateUnit{
		{Index: 0, LineIndex: 1, Speaker: "湊", Text: "「テスト」"},
	}
	translated := model.TranslateResponse{
		{Index: 0, Text: "「測試」"},
	}
	entities := []model.Entity{
		{
			Canonical: model.NamePair{From: "A", To: "甲"},
			Aliases:   []model.NamePair{{From: "湊", To: "錯誤別名"}},
		},
		{
			Canonical: model.NamePair{From: "湊", To: "正式湊"},
		},
	}

	got, err := Assemble(source, translated, nil, entities)
	if err != nil {
		t.Fatalf("Assemble() failed: %v", err)
	}
	want := "<NAME_PLATE>正式湊\n「測試」\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAssembleCrossScriptMetadataReuse(t *testing.T) {
	baseRaw := "<NAME_PLATE>A\n「台詞1」\n「台詞2」\n<PAUSE>\n"
	variantRaw := "// extra comment\n<BG>night\n<NAME_PLATE>A\n「台詞1」\n<VOICE>v01\n<NAME_PLATE>A\n「台詞2」\n<PAUSE>\n"

	baseScript, err := ParseBytes([]byte(baseRaw))
	if err != nil {
		t.Fatal(err)
	}
	variantScript, err := ParseBytes([]byte(variantRaw))
	if err != nil {
		t.Fatal(err)
	}

	baseExt, err := ExtractScript("base.txt", baseScript)
	if err != nil {
		t.Fatal(err)
	}
	variantExt, err := ExtractScript("variant.txt", variantScript)
	if err != nil {
		t.Fatal(err)
	}
	if baseExt.Hashsum != variantExt.Hashsum {
		t.Fatalf("expected identical hashsum: %s vs %s", baseExt.Hashsum, variantExt.Hashsum)
	}

	// Reuse translations indexed sequentially from baseExt onto variantExt's skeleton.
	reused := model.TranslateResponse{
		{Index: 0, Text: "「譯文1」"},
		{Index: 1, Text: "「譯文2」"},
	}
	got, err := Assemble(variantExt.Dialogue, reused, RetainedToUnits(variantExt.Retained), nil)
	if err != nil {
		t.Fatalf("Assemble() reuse failed: %v", err)
	}
	want := "// extra comment\n<BG>night\n<NAME_PLATE>A\n「譯文1」\n<VOICE>v01\n<NAME_PLATE>A\n「譯文2」\n<PAUSE>\n"
	if got != want {
		t.Fatalf("reused Assemble() mismatch:\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestAssembleValidationErrors(t *testing.T) {
	validSource := []model.TranslateUnit{
		{Index: 0, LineIndex: 2, Speaker: "A", Text: "こんにちは"},
		{Index: 1, LineIndex: 5, Speaker: "", Text: "ト書き"},
	}

	cases := []struct {
		name       string
		source     []model.TranslateUnit
		translated model.TranslateResponse
		retained   []model.Unit
		wantErr    string
	}{
		{
			name:   "missing translated index",
			source: validSource,
			translated: model.TranslateResponse{
				{Index: 0, Text: "你好"},
			},
			wantErr: "missing translated index 1",
		},
		{
			name:   "unknown translated index",
			source: validSource,
			translated: model.TranslateResponse{
				{Index: 0, Text: "你好"},
				{Index: 1, Text: "旁白"},
				{Index: 99, Text: "多餘"},
			},
			wantErr: "unknown index 99",
		},
		{
			name:   "duplicate translated index",
			source: validSource,
			translated: model.TranslateResponse{
				{Index: 0, Text: "你好"},
				{Index: 0, Text: "重複"},
				{Index: 1, Text: "旁白"},
			},
			wantErr: "duplicate translated index 0",
		},
		{
			name: "duplicate source index",
			source: []model.TranslateUnit{
				{Index: 2, LineIndex: 2, Speaker: "A", Text: "a"},
				{Index: 2, LineIndex: 5, Speaker: "B", Text: "b"},
			},
			translated: model.TranslateResponse{
				{Index: 2, Text: "甲"},
			},
			wantErr: "duplicate source index 2",
		},
		{
			name:   "blank text",
			source: validSource,
			translated: model.TranslateResponse{
				{Index: 0, Text: "  \t "},
				{Index: 1, Text: "旁白"},
			},
			wantErr: "blank text",
		},
		{
			name:   "nil retained unit",
			source: validSource,
			translated: model.TranslateResponse{
				{Index: 0, Text: "你好"},
				{Index: 1, Text: "旁白"},
			},
			retained: []model.Unit{nil},
			wantErr:  "retained unit 0 is nil",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Assemble(tc.source, tc.translated, tc.retained, nil)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got: %v", tc.wantErr, err)
			}
		})
	}
}
