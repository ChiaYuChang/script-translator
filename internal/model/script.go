package model

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

// ScriptLine is one source line. Index is the original 0-based line number.
type ScriptLine struct {
	Index   int
	Type    ScriptLineType
	Label   string
	Content string
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
