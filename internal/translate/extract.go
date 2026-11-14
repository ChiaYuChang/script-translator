package translate

import (
	"fmt"
	"strings"

	"go-script-translate/internal/model"
)

func Extract(script model.Script) ([]model.TranslateUnit, []model.Unit, error) {
	translations := make([]model.TranslateUnit, 0)
	retained := make([]model.Unit, 0, len(script.Lines))

	for i := 0; i < len(script.Lines); {
		line := script.Lines[i]

		if line.Type != model.ScriptLineTypeCmd ||
			line.Label != "NAME_PLATE" {
			if line.Type == model.ScriptLineTypeText {
				return nil, nil, fmt.Errorf(
					"line %d: text without NAME_PLATE",
					line.Index,
				)
			}

			retained = append(retained, line)
			i++
			continue
		}

		speaker := strings.TrimSpace(
			strings.TrimPrefix(line.Content, "<NAME_PLATE>"),
		)

		// Collect text until the next command or note.
		var parts []string
		j := i + 1
		for ; j < len(script.Lines); j++ {
			next := script.Lines[j]
			if next.Type != model.ScriptLineTypeText &&
				next.Type != model.ScriptLineTypeEmpty {
				break
			}
			if next.Type == model.ScriptLineTypeText {
				parts = append(parts, next.Content)
			}
		}

		// Preserve NAME_PLATE if it has no text.
		if len(parts) == 0 {
			retained = append(retained, line)
			i++
			continue
		}

		content := strings.Join(parts, "")
		if speaker == "" {
			// OS: no enclosing dialogue quotes.
			if strings.HasPrefix(content, "「") ||
				strings.HasSuffix(content, "」") {
				return nil, nil, fmt.Errorf(
					"line %d: invalid OS format",
					line.Index,
				)
			}
			translations = append(translations, model.TranslateUnit{
				Index:     len(translations),
				LineIndex: line.Index,
				Speaker:   speaker,
				Text:      content,
			})
		} else {
			// Dialogue: each 「」 utterance becomes one independent TranslateUnit.
			texts, err := extractDialogue(content)
			if err != nil {
				return nil, nil, fmt.Errorf(
					"line %d: %w",
					line.Index, err,
				)
			}
			for _, seg := range texts {
				translations = append(translations, model.TranslateUnit{
					Index:     len(translations),
					LineIndex: line.Index,
					Speaker:   speaker,
					Text:      seg,
				})
			}
		}
		i = j
	}

	if len(translations) == 0 {
		return nil, nil, ErrNoTranslationUnit
	}
	return translations, retained, nil
}

// ExtractScript parses dialogue utterances, retained skeleton lines, and the
// transcript SHA-256 hashsum into a portable ExtractedScript payload.
func ExtractScript(inputPath string, script model.Script) (model.ExtractedScript, error) {
	units, retainedUnits, err := Extract(script)
	if err != nil {
		return model.ExtractedScript{}, err
	}
	retained := make([]model.ScriptLine, len(retainedUnits))
	for i, u := range retainedUnits {
		sl, ok := u.(model.ScriptLine)
		if !ok {
			return model.ExtractedScript{}, fmt.Errorf("extract: unexpected retained unit %T", u)
		}
		retained[i] = sl
	}
	return model.ExtractedScript{
		Input:    inputPath,
		Hashsum:  HashsumFromUnits(units),
		Dialogue: units,
		Retained: retained,
	}, nil
}

// RetainedToUnits converts persisted retained script lines into Assemble units.
func RetainedToUnits(lines []model.ScriptLine) []model.Unit {
	out := make([]model.Unit, len(lines))
	for i, line := range lines {
		out[i] = line
	}
	return out
}

func extractDialogue(text string) ([]string, error) {
	matches := RegexText.FindAllStringSubmatchIndex(text, -1)
	if len(matches) == 0 {
		return nil, fmt.Errorf(
			"%w: no valid dialogue block found in %q",
			ErrInvalidDialogueFormat,
			text,
		)
	}

	dialogues := make([]string, 0, len(matches))
	pos := 0

	for i, match := range matches {
		// Every dialogue block must immediately follow
		// the previous block without unexpected content.
		if match[0] != pos {
			return nil, fmt.Errorf(
				"%w: unexpected content before dialogue %d at byte [%d:%d]: %q",
				ErrInvalidDialogueFormat,
				i+1,
				pos,
				match[0],
				text[pos:match[0]],
			)
		}

		dialogues = append(dialogues, text[match[2]:match[3]])
		pos = match[1]
	}

	// Ensure the entire input was consumed.
	if pos != len(text) {
		return nil, fmt.Errorf(
			"%w: unexpected trailing content at byte [%d:%d]: %q",
			ErrInvalidDialogueFormat,
			pos,
			len(text),
			text[pos:],
		)
	}

	return dialogues, nil
}
