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
		{Index: 0, LineIndex: 2, Speaker: "A", Text: "AAAABBBB"},
		{Index: 1, LineIndex: 2, Speaker: "A", Text: "CCCC"},
		{Index: 2, LineIndex: 8, Speaker: "", Text: "第一行。第二行。"},
		{Index: 3, LineIndex: 12, Speaker: "B", Text: "DDD"},
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
	want := []model.TranslateUnit{{Index: 0, LineIndex: 0, Speaker: "A", Text: "AAABBB"}}
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
	want := []model.TranslateUnit{{Index: 0, LineIndex: 2, Speaker: "B", Text: "AAA"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if len(retained) != 2 || retained[0].Order() != 0 || retained[1].Order() != 1 {
		t.Errorf("unexpected retained: %#v", retained)
	}
}

func TestExtractInvalidDialogue(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		reason string
	}{
		{"missing opening", "AAA」", "no valid dialogue block"},
		{"missing closing", "「AAA", "no valid dialogue block"},
		{"empty dialogue", "「」", "no valid dialogue block"},
		{"leading text", "XYZ「AAA」", "unexpected content before dialogue 1"},
		{"text between blocks", "「AAA」XYZ「BBB」", "unexpected content before dialogue 2"},
		{"trailing text", "「AAA」XYZ", "unexpected trailing content"},
		{"unclosed second block", "「AAA」「BBB", "unexpected trailing content"},
		{"nested opening quote", "「AAA「BBB」", "unexpected content before dialogue 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := "<NAME_PLATE>A\n" + tt.text + "\n<PAUSE>\n"
			_, _, err := extractSource(t, source)
			if !errors.Is(err, ErrInvalidDialogueFormat) {
				t.Fatalf("expected ErrInvalidDialogueFormat, got %v", err)
			}
			if !strings.Contains(err.Error(), "line 0:") || !strings.Contains(err.Error(), tt.reason) {
				t.Errorf("error lacks line or reason: %v", err)
			}
		})
	}
}

func TestExtractInvalidOS(t *testing.T) {
	for _, text := range []string{"「AAA」", "AAA」", "「AAA"} {
		t.Run(text, func(t *testing.T) {
			_, _, err := extractSource(t, "<NAME_PLATE>\n"+text+"\n")
			if err == nil || !strings.Contains(err.Error(), "line 0: invalid OS format") {
				t.Errorf("expected invalid OS format, got %v", err)
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
