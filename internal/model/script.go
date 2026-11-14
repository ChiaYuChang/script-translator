package model

import "fmt"

// Script is a parsed script. N is the number of source lines.
type Script struct {
	N     int
	Lines []ScriptLine
}

type ScriptLineType uint8

const (
	ScriptLineTypeEmpty ScriptLineType = iota
	ScriptLineTypeNote
	ScriptLineTypeCmd
	ScriptLineTypeText
)

func (t ScriptLineType) String() string {
	switch t {
	case ScriptLineTypeEmpty:
		return "EMPTY"
	case ScriptLineTypeNote:
		return "NOTE"
	case ScriptLineTypeCmd:
		return "CMD"
	case ScriptLineTypeText:
		return "TEXT"
	}
	return "UNKNOWN"
}

// MarshalText encodes ScriptLineType as its symbolic name for JSON persistence.
func (t ScriptLineType) MarshalText() ([]byte, error) {
	s := t.String()
	if s == "UNKNOWN" {
		return nil, fmt.Errorf("unknown script line type %d", t)
	}
	return []byte(s), nil
}

// UnmarshalText decodes ScriptLineType from its symbolic name.
func (t *ScriptLineType) UnmarshalText(text []byte) error {
	switch string(text) {
	case "EMPTY":
		*t = ScriptLineTypeEmpty
	case "NOTE":
		*t = ScriptLineTypeNote
	case "CMD":
		*t = ScriptLineTypeCmd
	case "TEXT":
		*t = ScriptLineTypeText
	default:
		return fmt.Errorf("unknown script line type %q", string(text))
	}
	return nil
}

// ScriptLine is one source line. Index is the original 0-based line number.
type ScriptLine struct {
	Index   int            `json:"index"`
	Type    ScriptLineType `json:"type"`
	Label   string         `json:"label"`
	Content string         `json:"content"`
}

// Unit is an item that can be ordered and written back to a script.
// Order is the source position, not a requirement to preserve line counts.
type Unit interface {
	Order() int
	String() string
}

func (l ScriptLine) Order() int { return l.Index }

// String writes the original line, using LF as the output line ending.
func (l ScriptLine) String() string { return l.Content + "\n" }

var _ Unit = ScriptLine{}
