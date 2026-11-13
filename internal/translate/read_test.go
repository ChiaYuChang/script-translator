package translate

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go-script-translate/internal/model"
)

func writeScript(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "script.txt")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRead(t *testing.T) {
	const source = "\ufeff  //作者\r\n" +
		"//【背景：DAILY_001】\r\n" +
		" <NAME_PLATE>新堂ミナト \r\n" +
		"  「それは言わないで。 \r\n" +
		"　時計を見るたびに、\r\n" +
		"　時間が止まってる気がするんだ」\r\n" +
		"<PAUSE>\r\n" +
		"\r\n" +
		"<NAME_PLATE>\r\n" +
		"　昼休みの少し前。\r\n"

	got, err := Read(writeScript(t, source))
	if err != nil {
		t.Fatal(err)
	}

	want := model.Script{
		N: 10,
		Lines: []model.ScriptLine{
			{
				Index:   0,
				Type:    model.ScriptLineTypeNote,
				Label:   "NOTE",
				Content: "//作者",
			},
			{
				Index:   1,
				Type:    model.ScriptLineTypeNote,
				Label:   "NOTE",
				Content: "//【背景：DAILY_001】",
			},
			{
				Index:   2,
				Type:    model.ScriptLineTypeCmd,
				Label:   "NAME_PLATE",
				Content: "<NAME_PLATE>新堂ミナト",
			},
			{
				Index:   3,
				Type:    model.ScriptLineTypeText,
				Label:   "TEXT",
				Content: "「それは言わないで。",
			},
			{
				Index:   4,
				Type:    model.ScriptLineTypeText,
				Label:   "TEXT",
				Content: "時計を見るたびに、",
			},
			{
				Index:   5,
				Type:    model.ScriptLineTypeText,
				Label:   "TEXT",
				Content: "時間が止まってる気がするんだ」",
			},
			{
				Index:   6,
				Type:    model.ScriptLineTypeCmd,
				Label:   "PAUSE",
				Content: "<PAUSE>",
			},
			{
				Index:   7,
				Type:    model.ScriptLineTypeEmpty,
				Label:   "EMPTY",
				Content: "",
			},
			{
				Index:   8,
				Type:    model.ScriptLineTypeCmd,
				Label:   "NAME_PLATE",
				Content: "<NAME_PLATE>",
			},
			{
				Index:   9,
				Type:    model.ScriptLineTypeText,
				Label:   "TEXT",
				Content: "昼休みの少し前。",
			},
		},
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("Read() mismatch:\ngot:  %#v\nwant: %#v", got, want)
	}
}

func TestReadEmpty(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{"empty file", ""},
		{"spaces", "   "},
		{"newlines", "\n\n\n"},
		{"whitespace", " \t\r\n　\r\n "},
		{"BOM only", "\ufeff"},
		{"BOM and whitespace", "\ufeff \r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Read(writeScript(t, tt.source))

			if !errors.Is(err, ErrEmptyScript) {
				t.Fatalf("expected ErrEmptyScript, got %v", err)
			}

			if got.N != 0 || len(got.Lines) != 0 {
				t.Errorf("expected empty script, got %+v", got)
			}
		})
	}
}

func TestReadTerminalNewline(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{"no newline", "<PAUSE>"},
		{"LF", "<PAUSE>\n"},
		{"CRLF", "<PAUSE>\r\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Read(writeScript(t, tt.source))
			if err != nil {
				t.Fatal(err)
			}

			want := model.Script{
				N: 1,
				Lines: []model.ScriptLine{
					{
						Index:   0,
						Type:    model.ScriptLineTypeCmd,
						Label:   "PAUSE",
						Content: "<PAUSE>",
					},
				},
			}

			if !reflect.DeepEqual(got, want) {
				t.Errorf("Read() mismatch:\ngot:  %#v\nwant: %#v", got, want)
			}
		})
	}
}

func TestReadClosingCommandTag(t *testing.T) {
	const source = "<GROUP>\n<ACTOR_OUT>0,LEFT,linear,500\n</GROUP>\n"

	got, err := Read(writeScript(t, source))
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Lines) != 3 {
		t.Fatalf("expected 3 lines, got %d", len(got.Lines))
	}
	if got.Lines[2].Type != model.ScriptLineTypeCmd || got.Lines[2].Label != "/GROUP" || got.Lines[2].Content != "</GROUP>" {
		t.Fatalf("unexpected closing tag line: %#v", got.Lines[2])
	}
}

func TestReadMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.txt")

	got, err := Read(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected os.ErrNotExist, got %v", err)
	}

	if got.N != 0 || len(got.Lines) != 0 {
		t.Errorf("expected empty script, got %+v", got)
	}
}
