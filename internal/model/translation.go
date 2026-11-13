package model

import "strings"

// TranslateUnit is a text block extracted from the source script.
// Index is the original order key of the block's first line.
// An empty Speaker means OS (narration).
// Text does not contain <NAME_PLATE> or outer Japanese quotation marks.
type TranslateUnit struct {
	Index   int      `json:"index"`
	Speaker string   `json:"name"`
	Text    []string `json:"text"`
}

func (t TranslateUnit) Transcript() string {
	sb := &strings.Builder{}
	speaker := t.Speaker
	if speaker == "" {
		speaker = "OS"
	}

	for i, text := range t.Text {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(speaker)
		sb.WriteByte(':')
		sb.WriteString(text)
	}
	return sb.String()
}

// TranslateRequest contains all source blocks and optional glossary entries.
type TranslateRequest struct {
	Entity   []Entity        `json:"entity"`
	Dialogue []TranslateUnit `json:"dialogue"`
}

// Transcript formats all non-empty dialogue blocks as speaker-prefixed lines
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

// TranslateBatchInput is the portion of the request sent in one LLM call.
type TranslateBatchInput struct {
	Entity   []Entity        `json:"entity"`
	Dialogue []TranslateUnit `json:"dialogue"`
}

// TranslatedText is the minimal response expected from the LLM.
// Speaker is recovered locally by Index; the LLM never needs to return it.
type TranslatedText struct {
	Index int      `json:"index"`
	Text  []string `json:"text"`
}

func (t TranslatedText) Transcript(speaker string) string {
	sb := &strings.Builder{}
	if speaker == "" {
		speaker = "OS"
	}

	for i, text := range t.Text {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(speaker)
		sb.WriteByte(':')
		sb.WriteString(text)
	}
	return sb.String()
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
