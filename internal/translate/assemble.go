package translate

import (
	"fmt"
	"sort"
	"strings"

	"go-script-translate/internal/model"
)

// Assemble merges translated dialogue units, retained script lines, and entity
// speaker mappings into the final script text without performing I/O.
func Assemble(
	source []model.TranslateUnit,
	translated model.TranslateResponse,
	retained []model.Unit,
	entities []model.Entity,
) (string, error) {
	sourceIndices := make(map[int]struct{}, len(source))
	for _, unit := range source {
		if _, dup := sourceIndices[unit.Index]; dup {
			return "", fmt.Errorf("assemble: duplicate source index %d", unit.Index)
		}
		sourceIndices[unit.Index] = struct{}{}
	}

	translatedByIndex := make(map[int]model.TranslatedText, len(translated))
	for _, item := range translated {
		if _, ok := sourceIndices[item.Index]; !ok {
			return "", fmt.Errorf("assemble: unknown index %d", item.Index)
		}
		if _, dup := translatedByIndex[item.Index]; dup {
			return "", fmt.Errorf("assemble: duplicate translated index %d", item.Index)
		}
		if strings.TrimSpace(item.Text) == "" {
			return "", fmt.Errorf("assemble: index %d has blank text", item.Index)
		}
		translatedByIndex[item.Index] = item
	}

	speakerMap := buildSpeakerMap(entities)

	units := make([]model.Unit, 0, len(retained)+len(source))
	for i, unit := range retained {
		if unit == nil {
			return "", fmt.Errorf("assemble: retained unit %d is nil", i)
		}
		units = append(units, unit)
	}

	var blocks []*model.TranslatedUnit
	var lastRawSpeaker string
	for _, unit := range source {
		tr, ok := translatedByIndex[unit.Index]
		if !ok {
			return "", fmt.Errorf("assemble: missing translated index %d", unit.Index)
		}
		speaker := ""
		if unit.Speaker != "" {
			if mapped, found := speakerMap[unit.Speaker]; found {
				speaker = mapped
			} else {
				speaker = unit.Speaker
			}
		}

		// Re-group consecutive utterances belonging to the same source <NAME_PLATE>
		// line so multi-sentence dialogue renders under one speaker header.
		if len(blocks) > 0 && blocks[len(blocks)-1].Index == unit.LineIndex && lastRawSpeaker == unit.Speaker {
			last := blocks[len(blocks)-1]
			last.Text = append(last.Text, tr.Text)
			continue
		}
		blocks = append(blocks, &model.TranslatedUnit{
			Index:   unit.LineIndex,
			Speaker: speaker,
			Text:    []string{tr.Text},
		})
		lastRawSpeaker = unit.Speaker
	}

	for _, b := range blocks {
		units = append(units, *b)
	}

	sort.SliceStable(units, func(i, j int) bool {
		return units[i].Order() < units[j].Order()
	})

	var sb strings.Builder
	for _, unit := range units {
		sb.WriteString(unit.String())
	}
	return sb.String(), nil
}

// buildSpeakerMap builds an exact-match lookup table from Japanese speaker
// names (canonical first, then aliases) to their Traditional Chinese names.
func buildSpeakerMap(entities []model.Entity) map[string]string {
	speakers := make(map[string]string, len(entities)*2)
	for _, e := range entities {
		if e.Canonical.From != "" && e.Canonical.To != "" {
			if _, exists := speakers[e.Canonical.From]; !exists {
				speakers[e.Canonical.From] = e.Canonical.To
			}
		}
	}
	for _, e := range entities {
		for _, alias := range e.Aliases {
			if alias.From != "" && alias.To != "" {
				if _, exists := speakers[alias.From]; !exists {
					speakers[alias.From] = alias.To
				}
			}
		}
	}
	return speakers
}
