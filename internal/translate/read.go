package translate

import (
	"bytes"
	"fmt"
	"os"

	"go-script-translate/internal/model"
)

// Read loads a script, splits it into lines, and classifies each line.
// Text is trimmed per line; the original line order is kept.
func Read(path string) (model.Script, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return model.Script{}, fmt.Errorf("read script %q: %w", path, err)
	}
	data = bytes.TrimPrefix(data, []byte{0xEF, 0xBB, 0xBF})

	if len(bytes.TrimSpace(data)) == 0 {
		return model.Script{}, fmt.Errorf("read script %q: %w", path, ErrEmptyScript)
	}
	raw := bytes.Split(data, []byte("\n"))
	if len(raw) > 0 && len(raw[len(raw)-1]) == 0 {
		raw = raw[:len(raw)-1]
	}

	script := model.Script{
		N:     len(raw),
		Lines: make([]model.ScriptLine, 0, len(raw)),
	}
	for i, line := range raw {
		content := string(bytes.TrimSpace(line))
		lType, lLabel := classifyLine(content)
		script.Lines = append(
			script.Lines,
			model.ScriptLine{
				Index:   i,
				Type:    lType,
				Label:   lLabel,
				Content: content,
			})
	}
	return script, nil
}

func classifyLine(content string) (model.ScriptLineType, string) {
	if content == "" {
		return model.ScriptLineTypeEmpty, model.ScriptLineTypeEmpty.String()
	}

	if RegexNote.MatchString(content) {
		return model.ScriptLineTypeNote, model.ScriptLineTypeNote.String()
	}

	if match := RegexCmd.FindStringSubmatch(content); match != nil {
		return model.ScriptLineTypeCmd, match[1]
	}

	return model.ScriptLineTypeText, model.ScriptLineTypeText.String()
}
