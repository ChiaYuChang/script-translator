package model

import "strings"

// TranslateUnit is one dialogue utterance or narration block extracted from a script.
// Index is the 0-based sequential utterance index across the script (stable across
// metadata edits); LineIndex is the 0-based source line number of its owning <NAME_PLATE>.
// An empty Speaker means OS (narration).
// Text does not contain <NAME_PLATE> or outer Japanese quotation marks.
type TranslateUnit struct {
	Index     int    `json:"index"`
	LineIndex int    `json:"line_index"`
	Speaker   string `json:"name"`
	Text      string `json:"text"`
}

func (t TranslateUnit) Transcript() string {
	if t.Text == "" {
		return ""
	}
	speaker := t.Speaker
	if speaker == "" {
		speaker = "OS"
	}
	return speaker + ":" + t.Text
}

// TranslateRequest contains all source blocks and optional glossary entries.
type TranslateRequest struct {
	Entity   []Entity        `json:"entity"`
	Dialogue []TranslateUnit `json:"dialogue"`
}

// Transcript formats all non-empty dialogue utterances as speaker-prefixed lines
// separated by newlines.
func (r TranslateRequest) Transcript() string {
	var sb strings.Builder
	for _, unit := range r.Dialogue {
		if text := unit.Transcript(); text != "" {
			if sb.Len() != 0 {
				sb.WriteByte('\n')
			}
			sb.WriteString(text)
		}
	}
	return sb.String()
}

// TranslatedText is the minimal response expected from the LLM for one utterance.
// Speaker is recovered locally by Index; the LLM never needs to return it.
type TranslatedText struct {
	Index int    `json:"index"`
	Text  string `json:"text"`
}

func (t TranslatedText) Transcript(speaker string) string {
	if speaker == "" {
		speaker = "OS"
	}
	return speaker + ":" + t.Text
}

// ExtractedScript is the portable intermediate artifact produced by `script extract`.
type ExtractedScript struct {
	Input    string          `json:"input,omitzero"`
	Hashsum  string          `json:"hashsum"`
	Dialogue []TranslateUnit `json:"dialogue"`
	Retained []ScriptLine    `json:"retained"`
}

// TranslatedScript is the intermediate translation artifact produced by `script translate`
// and reused across scripts sharing the same transcript Hashsum.
type TranslatedScript struct {
	Hashsum  string           `json:"hashsum"`
	Dialogue []TranslatedText `json:"dialogue"`
}

// TranslateBatchOutput is a top-level JSON array returned by an LLM batch.
type TranslateBatchOutput []TranslatedText

// TranslateResponse is the merged output from all translation batches.
type TranslateResponse []TranslatedText

// TranslatedUnit is a locally assembled, renderable unit.
// Speaker must already contain its translated display name, if any.
type TranslatedUnit struct {
	Index   int
	Speaker string
	Text    []string
}

func (u TranslatedUnit) Order() int {
	return u.Index
}

func (u TranslatedUnit) String() string {
	b := &strings.Builder{}
	b.WriteString("<NAME_PLATE>")
	b.WriteString(u.Speaker)
	b.WriteByte('\n')

	for _, text := range u.Text {
		if u.Speaker != "" {
			b.WriteString("「")
			b.WriteString(text)
			b.WriteString("」")
		} else {
			b.WriteString(text)
		}
		b.WriteByte('\n')
	}
	return b.String()
}
