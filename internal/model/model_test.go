package model

import (
	"sort"
	"strings"
	"testing"
)

func TestUnitRenderingPreservesOrderNotLineCount(t *testing.T) {
	units := []Unit{
		ScriptLine{Index: 15, Type: ScriptLineTypeCmd, Content: "<PAUSE>"},
		TranslatedUnit{Index: 11, Speaker: "新堂湊", Text: []string{"別提醒我啦。每次看時鐘，都覺得時間根本停住了。"}},
		ScriptLine{Index: 10, Type: ScriptLineTypeCmd, Content: "<PAUSE>"},
	}
	sort.SliceStable(units, func(i, j int) bool { return units[i].Order() < units[j].Order() })
	var b strings.Builder
	for _, u := range units {
		b.WriteString(u.String())
	}
	want := "<PAUSE>\n<NAME_PLATE>新堂湊\n「別提醒我啦。每次看時鐘，都覺得時間根本停住了。」\n<PAUSE>\n"
	if got := b.String(); got != want {
		t.Fatalf("rebuild mismatch:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestOSRendering(t *testing.T) {
	u := TranslatedUnit{Index: 1, Text: []string{"兩人相視而笑。"}}
	if got, want := u.String(), "<NAME_PLATE>\n兩人相視而笑。\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTranslateRequestTranscript(t *testing.T) {
	req := TranslateRequest{
		Dialogue: []TranslateUnit{
			{Index: 0, Speaker: "新堂ミナト", Text: []string{"一行目", "二行目"}},
			{Index: 1, Speaker: "", Text: nil}, // empty block is skipped
			{Index: 2, Speaker: "", Text: []string{"モノローグ"}},
		},
	}
	want := "新堂ミナト:一行目\n新堂ミナト:二行目\nOS:モノローグ"
	if got := req.Transcript(); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestEntityString(t *testing.T) {
	withAlias := Entity{
		ID:        1,
		Canonical: NamePair{From: "新堂ミナト", To: "新堂湊"},
		Aliases:   []NamePair{{From: "ミナト", To: "湊"}},
	}
	if got, want := withAlias.String(), "- 新堂ミナト: 新堂湊\n  - ミナト: 湊"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	withoutAlias := Entity{
		Canonical: NamePair{From: "朝倉ユイ", To: "朝倉由依"},
	}
	if got, want := withoutAlias.String(), "- 朝倉ユイ: 朝倉由依"; got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}
