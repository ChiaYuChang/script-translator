package translate

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"go-script-translate/internal/model"
)

// Extract parses dialogue utterances and retained skeleton lines from script.
// Irregular speaker blocks that cannot be split into balanced 「...」/『...』/（...）
// utterances fall back to a single unit; use ExtractWithWarnings to inspect warnings.
func Extract(script model.Script) ([]model.TranslateUnit, []model.Unit, error) {
	units, retained, _, err := ExtractWithWarnings(script)
	return units, retained, err
}

// ExtractWithWarnings parses dialogue utterances and retained skeleton lines from
// script, returning non-fatal warnings when a dialogue block cannot be split by
// outer 「...」/『...』/（...） delimiters and falls back to a single TranslateUnit.
func ExtractWithWarnings(script model.Script) ([]model.TranslateUnit, []model.Unit, []string, error) {
	translations := make([]model.TranslateUnit, 0)
	retained := make([]model.Unit, 0, len(script.Lines))
	var warnings []string

	for i := 0; i < len(script.Lines); {
		line := script.Lines[i]

		if line.Type != model.ScriptLineTypeCmd ||
			line.Label != "NAME_PLATE" {
			if line.Type == model.ScriptLineTypeText {
				return nil, nil, nil, fmt.Errorf(
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
		var texts []string
		if speaker == "" {
			// OS: normally plain narration, though unnamed/off-screen speech or narration quoting
			// 「...」/『...』 can also appear under an empty <NAME_PLATE>. Split balanced utterances cleanly;
			// otherwise keep the block as 1 unit (and warn on unbalanced/mixed leading/trailing quotes).
			if startsWithDialogueOpen(content) {
				if segs, err := extractDialogue(content); err == nil {
					texts = segs
				} else {
					warnings = append(warnings, fmt.Sprintf(
						"line %d (OS): could not split quoted text (%v); keeping block as 1 unit",
						line.Index, err,
					))
					texts = []string{content}
				}
			} else {
				if strings.HasSuffix(content, "」") || strings.HasSuffix(content, "』") {
					warnings = append(warnings, fmt.Sprintf(
						"line %d (OS): trailing closing quote under empty NAME_PLATE; keeping block as 1 unit",
						line.Index,
					))
				}
				texts = []string{content}
			}
		} else {
			// Dialogue: each 「...」, 『...』, or （...） utterance becomes one independent TranslateUnit.
			// If delimiters are unbalanced (for instance split by a command), fall back to
			// keeping the entire block as a single unit and emit a warning.
			var err error
			texts, err = extractDialogue(content)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf(
					"line %d (speaker %q): could not split utterances (%v); keeping block as 1 unit",
					line.Index, speaker, err,
				))
				texts = []string{content}
			}
		}
		for _, seg := range texts {
			translations = append(translations, model.TranslateUnit{
				Index:     len(translations),
				LineIndex: line.Index,
				Speaker:   speaker,
				Text:      seg,
			})
		}
		i = j
	}

	if len(translations) == 0 {
		return nil, nil, nil, ErrNoTranslationUnit
	}
	return translations, retained, warnings, nil
}

// ExtractScript parses dialogue utterances, retained skeleton lines, and the
// transcript SHA-256 hashsum into a portable ExtractedScript payload.
func ExtractScript(inputPath string, script model.Script) (model.ExtractedScript, error) {
	extracted, _, err := ExtractScriptWithWarnings(inputPath, script)
	return extracted, err
}

// ExtractScriptWithWarnings behaves like ExtractScript and additionally returns
// non-fatal dialogue-splitting fallback warnings.
func ExtractScriptWithWarnings(inputPath string, script model.Script) (model.ExtractedScript, []string, error) {
	units, retainedUnits, warnings, err := ExtractWithWarnings(script)
	if err != nil {
		return model.ExtractedScript{}, nil, err
	}
	retained := make([]model.ScriptLine, len(retainedUnits))
	for i, u := range retainedUnits {
		sl, ok := u.(model.ScriptLine)
		if !ok {
			return model.ExtractedScript{}, nil, fmt.Errorf("extract: unexpected retained unit %T", u)
		}
		retained[i] = sl
	}
	return model.ExtractedScript{
		Input:    inputPath,
		Hashsum:  HashsumFromUnits(units),
		Dialogue: units,
		Retained: retained,
	}, warnings, nil
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
	if text == "" {
		return nil, fmt.Errorf("%w: no valid dialogue block found in %q", ErrInvalidDialogueFormat, text)
	}

	var (
		dialogues []string
		stack     []rune
		openLen   int
		startByte = -1
	)

	for i, r := range text {
		if len(stack) == 0 {
			if unicode.IsSpace(r) {
				continue
			}
			expectedClose, isOpen := dialogueBrackets[r]
			if !isOpen {
				return nil, fmt.Errorf(
					"%w: unexpected content before dialogue %d at byte [%d:%d]: %q",
					ErrInvalidDialogueFormat,
					len(dialogues)+1,
					i,
					len(text),
					text[i:],
				)
			}
			stack = append(stack, expectedClose)
			openLen = utf8.RuneLen(r)
			startByte = i
			continue
		}

		if expectedClose, isOpen := dialogueBrackets[r]; isOpen {
			stack = append(stack, expectedClose)
			continue
		}

		if _, isClose := dialogueCloseBrackets[r]; isClose {
			if stack[len(stack)-1] != r {
				return nil, fmt.Errorf(
					"%w: mismatched closing bracket %q at byte %d in %q",
					ErrInvalidDialogueFormat,
					r,
					i,
					text,
				)
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				endByte := i + utf8.RuneLen(r)
				segment := text[startByte:endByte]
				inner := text[startByte+openLen : i]
				if strings.TrimSpace(inner) == "" {
					return nil, fmt.Errorf("%w: empty dialogue block %q", ErrInvalidDialogueFormat, segment)
				}
				dialogues = append(dialogues, segment)
				startByte = -1
				openLen = 0
			}
		}
	}

	if len(stack) != 0 {
		return nil, fmt.Errorf(
			"%w: unclosed dialogue block starting at byte %d: %q",
			ErrInvalidDialogueFormat,
			startByte,
			text[startByte:],
		)
	}
	if len(dialogues) == 0 {
		return nil, fmt.Errorf("%w: no valid dialogue block found in %q", ErrInvalidDialogueFormat, text)
	}

	return dialogues, nil
}

var dialogueBrackets = map[rune]rune{
	'（': '）',
	'「': '」',
	'『': '』',
}

var dialogueCloseBrackets = func() map[rune]rune {
	m := make(map[rune]rune, len(dialogueBrackets))
	for openRune, closeRune := range dialogueBrackets {
		m[closeRune] = openRune
	}
	return m
}()

func startsWithDialogueOpen(s string) bool {
	r, _ := utf8.DecodeRuneInString(s)
	_, ok := dialogueBrackets[r]
	return ok
}
