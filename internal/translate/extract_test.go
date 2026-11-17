package translate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"go-script-translate/internal/model"
)

func extractSource(t *testing.T, source string) ([]model.TranslateUnit, []model.Unit, error) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	script, err := Read(path)
	if err != nil {
		t.Fatalf("Read() error: %v", err)
	}
	return Extract(script)
}

func TestExtract(t *testing.T) {
	const source = `//【背景：DAILY_001】
<BG>nc_cafe_morning,IN,1000
<NAME_PLATE>A
「AAAA
 BBBB」
「CCCC」
<PAUSE>

<NAME_PLATE>
 第一行。
第二行。
<PAUSE>
<NAME_PLATE>B
「DDD」
<SCENARIO_END>
`

	got, retained, err := extractSource(t, source)
	if err != nil {
		t.Fatal(err)
	}

	want := []model.TranslateUnit{
		{Index: 0, LineIndex: 2, Speaker: "A", Text: "「AAAABBBB」", Bracket: "「"},
		{Index: 1, LineIndex: 2, Speaker: "A", Text: "「CCCC」", Bracket: "「"},
		{Index: 2, LineIndex: 8, Speaker: "", Text: "第一行。第二行。"},
		{Index: 3, LineIndex: 12, Speaker: "B", Text: "「DDD」", Bracket: "「"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("translations mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}

	wantRetained := []model.Unit{
		model.ScriptLine{Index: 0, Type: model.ScriptLineTypeNote, Label: "NOTE", Content: "//【背景：DAILY_001】"},
		model.ScriptLine{Index: 1, Type: model.ScriptLineTypeCmd, Label: "BG", Content: "<BG>nc_cafe_morning,IN,1000"},
		model.ScriptLine{Index: 6, Type: model.ScriptLineTypeCmd, Label: "PAUSE", Content: "<PAUSE>"},
		model.ScriptLine{Index: 7, Type: model.ScriptLineTypeEmpty, Label: "EMPTY", Content: ""},
		model.ScriptLine{Index: 11, Type: model.ScriptLineTypeCmd, Label: "PAUSE", Content: "<PAUSE>"},
		model.ScriptLine{Index: 14, Type: model.ScriptLineTypeCmd, Label: "SCENARIO_END", Content: "<SCENARIO_END>"},
	}
	if !reflect.DeepEqual(retained, wantRetained) {
		t.Errorf("retained mismatch:\ngot:  %#v\nwant: %#v", retained, wantRetained)
	}
}

func TestExtractEmptyLinesWithinDialogue(t *testing.T) {
	const source = "<NAME_PLATE>A\n「AAA\n\nBBB」\n<PAUSE>\n"

	got, retained, err := extractSource(t, source)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.TranslateUnit{{Index: 0, LineIndex: 0, Speaker: "A", Text: "「AAABBB」", Bracket: "「"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if len(retained) != 1 || retained[0].Order() != 4 || retained[0].String() != "<PAUSE>\n" {
		t.Errorf("unexpected retained: %#v", retained)
	}
}

func TestExtractNamePlateWithoutText(t *testing.T) {
	const source = "<NAME_PLATE>A\n<PAUSE>\n<NAME_PLATE>B\n「AAA」\n"

	got, retained, err := extractSource(t, source)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.TranslateUnit{{Index: 0, LineIndex: 2, Speaker: "B", Text: "「AAA」", Bracket: "「"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if len(retained) != 2 || retained[0].Order() != 0 || retained[1].Order() != 1 {
		t.Errorf("unexpected retained: %#v", retained)
	}
}

func TestExtractMonologueAndMixedBrackets(t *testing.T) {
	const source = `<NAME_PLATE>俺
（したいな……）
<PAUSE>
<NAME_PLATE>俺
（多行にわたる
モノローグもひとつに）
<PAUSE>
<NAME_PLATE>俺
「XXXX（OO）XXX」
（XXX「OO」）
「彼は「うん」と言った」
『XXX』『YYY』『ZZZ』
『AAA「BBB」』
<PAUSE>
<NAME_PLATE>ネット民
　『草』　『わかる』　『きたー！』
<PAUSE>
`

	got, retained, err := extractSource(t, source)
	if err != nil {
		t.Fatal(err)
	}

	want := []model.TranslateUnit{
		{Index: 0, LineIndex: 0, Speaker: "俺", Text: "（したいな……）", Bracket: "（"},
		{Index: 1, LineIndex: 3, Speaker: "俺", Text: "（多行にわたるモノローグもひとつに）", Bracket: "（"},
		{Index: 2, LineIndex: 7, Speaker: "俺", Text: "「XXXX（OO）XXX」", Bracket: "「"},
		{Index: 3, LineIndex: 7, Speaker: "俺", Text: "（XXX「OO」）", Bracket: "（"},
		{Index: 4, LineIndex: 7, Speaker: "俺", Text: "「彼は「うん」と言った」", Bracket: "「"},
		{Index: 5, LineIndex: 7, Speaker: "俺", Text: "『XXX』", Bracket: "『"},
		{Index: 6, LineIndex: 7, Speaker: "俺", Text: "『YYY』", Bracket: "『"},
		{Index: 7, LineIndex: 7, Speaker: "俺", Text: "『ZZZ』", Bracket: "『"},
		{Index: 8, LineIndex: 7, Speaker: "俺", Text: "『AAA「BBB」』", Bracket: "『"},
		{Index: 9, LineIndex: 14, Speaker: "ネット民", Text: "『草』", Bracket: "『"},
		{Index: 10, LineIndex: 14, Speaker: "ネット民", Text: "『わかる』", Bracket: "『"},
		{Index: 11, LineIndex: 14, Speaker: "ネット民", Text: "『きたー！』", Bracket: "『"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("translations mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
	if len(retained) != 4 {
		t.Fatalf("expected 4 retained <PAUSE> lines, got %d", len(retained))
	}
}

func TestExtractInvalidDialogue(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		reason string
	}{
		{"missing opening", "AAA」", "unexpected content before dialogue 1"},
		{"missing closing", "「AAA", "unclosed dialogue block"},
		{"empty dialogue", "「」", "empty dialogue block"},
		{"empty double quote", "『』", "empty dialogue block"},
		{"unclosed double quote", "『AAA", "unclosed dialogue block"},
		{"mismatched double quote", "『AAA」", "mismatched closing bracket"},
		{"empty monologue", "（）", "empty dialogue block"},
		{"unclosed monologue", "（AAA", "unclosed dialogue block"},
		{"missing opening monologue", "AAA）", "unexpected content before dialogue 1"},
		{"mismatched closing", "「AAA）", "mismatched closing bracket"},
		{"mismatched inner closing", "「AAA（BBB」", "mismatched closing bracket"},
		{"leading text", "XYZ「AAA」", "unexpected content before dialogue 1"},
		{"text between blocks", "「AAA」XYZ「BBB」", "unexpected content before dialogue 2"},
		{"trailing text", "「AAA」XYZ", "unexpected content before dialogue 2"},
		{"unclosed second block", "「AAA」「BBB", "unclosed dialogue block"},
		{"unclosed nested quote", "「AAA「BBB」", "unclosed dialogue block"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := extractDialogue(tt.text); !errors.Is(err, ErrInvalidDialogueFormat) || !strings.Contains(err.Error(), tt.reason) {
				t.Fatalf("extractDialogue(%q) expected ErrInvalidDialogueFormat containing %q, got %v", tt.text, tt.reason, err)
			}

			// ExtractWithWarnings falls back to keeping the entire block as 1 unit and emits a warning.
			script, err := ParseBytes([]byte("<NAME_PLATE>A\n" + tt.text + "\n<PAUSE>\n"))
			if err != nil {
				t.Fatal(err)
			}
			units, _, warnings, err := ExtractWithWarnings(script)
			if err != nil {
				t.Fatalf("expected fallback extraction to succeed, got %v", err)
			}
			wantUnit := []model.TranslateUnit{{Index: 0, LineIndex: 0, Speaker: "A", Text: tt.text}}
			if !reflect.DeepEqual(units, wantUnit) {
				t.Fatalf("fallback units mismatch: got %#v, want %#v", units, wantUnit)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "line 0") || !strings.Contains(warnings[0], tt.reason) {
				t.Fatalf("expected warning mentioning line 0 and %q, got %#v", tt.reason, warnings)
			}
		})
	}
}

func TestExtractSplitAcrossCommandEmitsWarningAndKeepsUnits(t *testing.T) {
	const source = `<NAME_PLATE>俺
（前半がコマンドで切られた……
<VOICE>vo_001
<NAME_PLATE>俺
後半部分）
<PAUSE>
`
	script, err := ParseBytes([]byte(source))
	if err != nil {
		t.Fatal(err)
	}
	units, retained, warnings, err := ExtractWithWarnings(script)
	if err != nil {
		t.Fatalf("ExtractWithWarnings failed: %v", err)
	}
	wantUnits := []model.TranslateUnit{
		{Index: 0, LineIndex: 0, Speaker: "俺", Text: "（前半がコマンドで切られた……"},
		{Index: 1, LineIndex: 3, Speaker: "俺", Text: "後半部分）"},
	}
	if !reflect.DeepEqual(units, wantUnits) {
		t.Fatalf("units mismatch:\ngot:  %#v\nwant: %#v", units, wantUnits)
	}
	if len(warnings) != 2 {
		t.Fatalf("expected 2 fallback warnings, got %#v", warnings)
	}
	if len(retained) != 2 || retained[0].String() != "<VOICE>vo_001\n" {
		t.Fatalf("unexpected retained units: %#v", retained)
	}
}

func TestExtractOSWithQuotes(t *testing.T) {
	t.Run("balanced unnamed quotes split cleanly without warnings", func(t *testing.T) {
		script, err := ParseBytes([]byte("<NAME_PLATE>\n「AAA」「BBB」\n<PAUSE>\n"))
		if err != nil {
			t.Fatal(err)
		}
		units, retained, warnings, err := ExtractWithWarnings(script)
		if err != nil {
			t.Fatalf("expected balanced quotes under empty NAME_PLATE to extract cleanly, got %v", err)
		}
		if len(warnings) != 0 {
			t.Fatalf("expected no warnings for balanced quotes, got %#v", warnings)
		}
		wantUnits := []model.TranslateUnit{
			{Index: 0, LineIndex: 0, Speaker: "", Text: "「AAA」", Bracket: "「"},
			{Index: 1, LineIndex: 0, Speaker: "", Text: "「BBB」", Bracket: "「"},
		}
		if !reflect.DeepEqual(units, wantUnits) {
			t.Fatalf("units mismatch:\ngot  %#v\nwant %#v", units, wantUnits)
		}
		if len(retained) != 1 || retained[0].String() != "<PAUSE>\n" {
			t.Fatalf("unexpected retained: %#v", retained)
		}

		assembled, err := Assemble(units, model.TranslateResponse{
			{Index: 0, Text: "「甲」"},
			{Index: 1, Text: "「乙」"},
		}, retained, nil)
		if err != nil {
			t.Fatalf("Assemble failed: %v", err)
		}
		wantAssembled := "<NAME_PLATE>\n「甲」\n「乙」\n<PAUSE>\n"
		if assembled != wantAssembled {
			t.Fatalf("assembled mismatch:\ngot:\n%s\nwant:\n%s", assembled, wantAssembled)
		}
	})

	for _, text := range []string{"「待って」と声がした。", "AAA」", "AAA』", "「AAA"} {
		t.Run("fallback_warning_"+text, func(t *testing.T) {
			script, err := ParseBytes([]byte("<NAME_PLATE>\n" + text + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			units, _, warnings, err := ExtractWithWarnings(script)
			if err != nil {
				t.Fatalf("expected fallback extraction to succeed, got %v", err)
			}
			wantUnits := []model.TranslateUnit{{Index: 0, LineIndex: 0, Speaker: "", Text: text}}
			if !reflect.DeepEqual(units, wantUnits) {
				t.Fatalf("units mismatch: got %#v, want %#v", units, wantUnits)
			}
			if len(warnings) != 1 || !strings.Contains(warnings[0], "line 0 (OS)") {
				t.Fatalf("expected 1 OS warning mentioning line 0, got %#v", warnings)
			}
		})
	}
}

func TestExtractOrphanText(t *testing.T) {
	_, _, err := extractSource(t, "「AAA」\n<PAUSE>\n")
	if err == nil || !strings.Contains(err.Error(), "line 0: text without NAME_PLATE") {
		t.Errorf("expected orphan text error, got %v", err)
	}
}

func TestExtractNoTranslationUnit(t *testing.T) {
	const source = "//note\n<BG>test\n<NAME_PLATE>A\n<PAUSE>\n"
	got, retained, err := extractSource(t, source)
	if !errors.Is(err, ErrNoTranslationUnit) {
		t.Fatalf("expected ErrNoTranslationUnit, got %v", err)
	}
	if got != nil || retained != nil {
		t.Errorf("expected nil results on error, got %#v and %#v", got, retained)
	}
}
