package entitystore

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go-script-translate/internal/model"
	"go-script-translate/internal/pathutil"
)

// Load reads and validates an entity mapping JSON file.
// If the file does not exist, it returns an empty mapping without error.
func Load(path string) (model.EntityMapping, error) {
	resolvedPath, err := pathutil.ExpandHome(path)
	if err != nil {
		return model.EntityMapping{}, fmt.Errorf("entitystore: resolve %q: %w", path, err)
	}
	data, err := os.ReadFile(resolvedPath) // #nosec G304 -- dictionary path is provided by CLI/user
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return model.EntityMapping{Entities: []model.Entity{}}, nil
		}
		return model.EntityMapping{}, fmt.Errorf("entitystore: read %q: %w", resolvedPath, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return model.EntityMapping{}, fmt.Errorf("entitystore: read %q: empty JSON file", resolvedPath)
	}

	var mapping model.EntityMapping
	if err := json.Unmarshal(data, &mapping); err != nil {
		return model.EntityMapping{}, fmt.Errorf("entitystore: parse %q: %w", resolvedPath, err)
	}
	if mapping.Entities == nil {
		return model.EntityMapping{}, fmt.Errorf("entitystore: parse %q: missing entities array", resolvedPath)
	}

	normalized, err := normalizeAndValidate(mapping, false)
	if err != nil {
		return model.EntityMapping{}, fmt.Errorf("entitystore: validate %q: %w", resolvedPath, err)
	}
	return normalized, nil
}

// Save atomically writes mapping as formatted JSON to path without mutating the input.
func Save(path string, mapping model.EntityMapping) error {
	resolvedPath, err := pathutil.ExpandHome(path)
	if err != nil {
		return fmt.Errorf("entitystore: resolve %q: %w", path, err)
	}
	normalized, err := normalizeAndValidate(mapping, false)
	if err != nil {
		return fmt.Errorf("entitystore: save %q: %w", resolvedPath, err)
	}

	dir := filepath.Dir(resolvedPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("entitystore: create directory %q: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, ".entities-*.tmp")
	if err != nil {
		return fmt.Errorf("entitystore: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()

	if err := json.MarshalWrite(tmp, normalized, jsontext.WithIndent("  ")); err != nil {
		return fmt.Errorf("entitystore: encode JSON: %w", err)
	}
	if _, err := tmp.WriteString("\n"); err != nil {
		return fmt.Errorf("entitystore: write trailing newline: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		return fmt.Errorf("entitystore: chmod temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("entitystore: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, resolvedPath); err != nil {
		return fmt.Errorf("entitystore: rename temp file to %q: %w", resolvedPath, err)
	}
	cleanup = false
	return nil
}

// ImportReviewed merges reviewed entity mappings into current with all-or-nothing
// semantics, assigning sequential IDs to new entities without mutating inputs.
func ImportReviewed(
	current model.EntityMapping,
	reviewed model.EntityMapping,
) (model.EntityMapping, error) {
	base, err := normalizeAndValidate(current, true)
	if err != nil {
		return model.EntityMapping{}, fmt.Errorf("entitystore: invalid current mapping: %w", err)
	}

	result := make([]model.Entity, 0, len(base.Entities)+len(reviewed.Entities))
	byCanonical := make(map[string]int, len(base.Entities))
	owner := make(map[string]string, len(base.Entities)*2)
	translationOf := make(map[string]string, len(base.Entities)*2)
	var maxID int64

	for _, e := range base.Entities {
		if e.ID > maxID {
			maxID = e.ID
		}
		byCanonical[e.Canonical.From] = len(result)
		owner[e.Canonical.From] = e.Canonical.From
		translationOf[e.Canonical.From] = e.Canonical.To
		for _, a := range e.Aliases {
			owner[a.From] = e.Canonical.From
			translationOf[a.From] = a.To
		}
		result = append(result, e)
	}

	seenReviewed := make(map[string]struct{}, len(reviewed.Entities))
	for _, rev := range reviewed.Entities {
		from := strings.TrimSpace(rev.Canonical.From)
		to := strings.TrimSpace(rev.Canonical.To)
		if from == "" || to == "" {
			return model.EntityMapping{}, errors.New("entitystore: reviewed entity has empty canonical name or translation")
		}
		if _, dup := seenReviewed[from]; dup {
			return model.EntityMapping{}, fmt.Errorf("entitystore: duplicate canonical %q in reviewed mapping", from)
		}
		seenReviewed[from] = struct{}{}

		if idx, exists := byCanonical[from]; exists {
			existing := &result[idx]
			if rev.ID != 0 && rev.ID != existing.ID {
				return model.EntityMapping{}, fmt.Errorf("entitystore: entity %q ID mismatch (got %d, want %d)", from, rev.ID, existing.ID)
			}
			if to != existing.Canonical.To {
				return model.EntityMapping{}, fmt.Errorf("entitystore: entity %q canonical translation conflict (got %q, want %q)", from, to, existing.Canonical.To)
			}
			if err := mergeAliases(existing, rev.Aliases, owner, translationOf); err != nil {
				return model.EntityMapping{}, err
			}
			continue
		}

		if rev.ID != 0 {
			return model.EntityMapping{}, fmt.Errorf("entitystore: new entity %q must not have non-zero ID %d", from, rev.ID)
		}
		if takenOwner, taken := owner[from]; taken {
			return model.EntityMapping{}, fmt.Errorf("entitystore: new canonical %q conflicts with existing alias of %q", from, takenOwner)
		}

		maxID++
		created := model.Entity{
			ID:        maxID,
			Canonical: model.NamePair{From: from, To: to},
			Aliases:   []model.NamePair{},
		}
		owner[from] = from
		translationOf[from] = to

		if err := mergeAliases(&created, rev.Aliases, owner, translationOf); err != nil {
			return model.EntityMapping{}, err
		}
		byCanonical[from] = len(result)
		result = append(result, created)
	}

	return model.EntityMapping{Entities: result}, nil
}

func mergeAliases(
	target *model.Entity,
	aliases []model.NamePair,
	owner map[string]string,
	translationOf map[string]string,
) error {
	canonical := target.Canonical.From
	for _, a := range aliases {
		from := strings.TrimSpace(a.From)
		to := strings.TrimSpace(a.To)
		if from == "" || to == "" {
			return fmt.Errorf("entitystore: entity %q has empty alias name or translation", canonical)
		}
		if takenOwner, taken := owner[from]; taken {
			if takenOwner != canonical {
				return fmt.Errorf("entitystore: alias %q for %q conflicts with entity %q", from, canonical, takenOwner)
			}
			if from == canonical {
				return fmt.Errorf("entitystore: alias %q cannot match canonical name", from)
			}
			if existingTo := translationOf[from]; existingTo != to {
				return fmt.Errorf("entitystore: alias %q translation conflict for %q (got %q, want %q)", from, canonical, to, existingTo)
			}
			continue
		}
		target.Aliases = append(target.Aliases, model.NamePair{From: from, To: to})
		owner[from] = canonical
		translationOf[from] = to
	}
	return nil
}

func normalizeAndValidate(mapping model.EntityMapping, requireIDs bool) (model.EntityMapping, error) {
	entities := make([]model.Entity, 0, len(mapping.Entities))
	seenIDs := make(map[int64]string, len(mapping.Entities))
	seenNames := make(map[string]string, len(mapping.Entities)*2)

	for i, e := range mapping.Entities {
		from := strings.TrimSpace(e.Canonical.From)
		to := strings.TrimSpace(e.Canonical.To)
		if from == "" || to == "" {
			return model.EntityMapping{}, fmt.Errorf("entity[%d]: empty canonical name or translation", i)
		}
		if e.ID < 0 {
			return model.EntityMapping{}, fmt.Errorf("entity %q: negative ID %d", from, e.ID)
		}
		if requireIDs && e.ID == 0 {
			return model.EntityMapping{}, fmt.Errorf("entity %q: missing persistent ID", from)
		}
		if e.ID > 0 {
			if prev, dup := seenIDs[e.ID]; dup {
				return model.EntityMapping{}, fmt.Errorf("entity %q: duplicate ID %d (already used by %q)", from, e.ID, prev)
			}
			seenIDs[e.ID] = from
		}
		if prev, dup := seenNames[from]; dup {
			return model.EntityMapping{}, fmt.Errorf("canonical %q conflicts with %q", from, prev)
		}
		seenNames[from] = from

		aliases := make([]model.NamePair, 0, len(e.Aliases))
		for _, a := range e.Aliases {
			afrom := strings.TrimSpace(a.From)
			ato := strings.TrimSpace(a.To)
			if afrom == "" || ato == "" {
				return model.EntityMapping{}, fmt.Errorf("entity %q: empty alias name or translation", from)
			}
			if prev, dup := seenNames[afrom]; dup {
				return model.EntityMapping{}, fmt.Errorf("alias %q for %q conflicts with %q", afrom, from, prev)
			}
			seenNames[afrom] = from
			aliases = append(aliases, model.NamePair{From: afrom, To: ato})
		}

		entities = append(entities, model.Entity{
			ID:        e.ID,
			Canonical: model.NamePair{From: from, To: to},
			Aliases:   aliases,
		})
	}

	return model.EntityMapping{Entities: entities}, nil
}
