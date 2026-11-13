package entitystore

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"go-script-translate/internal/model"
)

func TestLoadMissingAndEmptyMapping(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "does-not-exist.json")
	got, err := Load(missingPath)
	if err != nil {
		t.Fatalf("Load(missing) unexpected error: %v", err)
	}
	if got.Entities == nil || len(got.Entities) != 0 {
		t.Fatalf("expected non-nil empty entities slice, got %#v", got)
	}

	emptyMappingPath := filepath.Join(t.TempDir(), "entities.json")
	if err := os.WriteFile(emptyMappingPath, []byte(`{"entities":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err = Load(emptyMappingPath)
	if err != nil {
		t.Fatalf("Load(empty mapping) unexpected error: %v", err)
	}
	if got.Entities == nil || len(got.Entities) != 0 {
		t.Fatalf("expected non-nil empty entities slice, got %#v", got)
	}
}

func TestLoadRejectsInvalidJSONAndInvalidMappings(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{name: "empty file", content: "   \n"},
		{name: "corrupt JSON", content: `{"entities": [`},
		{name: "missing entities field", content: `{}`},
		{name: "null entities", content: `{"entities": null}`},
		{name: "negative ID", content: `{"entities":[{"id":-1,"canonical":{"from":"A","to":"甲"},"aliases":[]}]}`},
		{name: "duplicate ID", content: `{"entities":[{"id":1,"canonical":{"from":"A","to":"甲"},"aliases":[]},{"id":1,"canonical":{"from":"B","to":"乙"},"aliases":[]}]}`},
		{name: "empty canonical", content: `{"entities":[{"id":1,"canonical":{"from":"","to":"甲"},"aliases":[]}]}`},
		{name: "cross-entity alias collision", content: `{"entities":[{"id":1,"canonical":{"from":"A","to":"甲"},"aliases":[{"from":"B","to":"乙"}]} ,{"id":2,"canonical":{"from":"B","to":"乙"},"aliases":[]}]}`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "entities.json")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestSaveLoadRoundTripAndNonNullAliases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "entities.json")
	input := model.EntityMapping{
		Entities: []model.Entity{
			{
				ID:        1,
				Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
				Aliases:   nil, // should serialize as [] rather than null
			},
			{
				ID:        2,
				Canonical: model.NamePair{From: "朝倉ユイ", To: "朝倉由依"},
				Aliases:   []model.NamePair{{From: "ユイ", To: "由依"}},
			},
		},
	}

	if err := Save(path, input); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("unmarshal saved JSON: %v", err)
	}
	entitiesRaw, ok := doc["entities"].([]any)
	if !ok || entitiesRaw == nil || len(entitiesRaw) != 2 {
		t.Fatalf("expected non-null entities array of length 2, got %#v", doc["entities"])
	}
	firstEntity, ok := entitiesRaw[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected first entity JSON type: %#v", entitiesRaw[0])
	}
	aliasesRaw, exists := firstEntity["aliases"]
	if !exists {
		t.Fatalf("expected aliases key on first entity: %#v", firstEntity)
	}
	if aliasesSlice, ok := aliasesRaw.([]any); !ok || aliasesSlice == nil || len(aliasesSlice) != 0 {
		t.Fatalf("expected non-null empty aliases array [], got %#v", aliasesRaw)
	}

	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	want := model.EntityMapping{
		Entities: []model.Entity{
			{
				ID:        1,
				Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
				Aliases:   []model.NamePair{},
			},
			{
				ID:        2,
				Canonical: model.NamePair{From: "朝倉ユイ", To: "朝倉由依"},
				Aliases:   []model.NamePair{{From: "ユイ", To: "由依"}},
			},
		},
	}
	if !reflect.DeepEqual(loaded, want) {
		t.Fatalf("round-trip mismatch:\ngot  %#v\nwant %#v", loaded, want)
	}
}

func TestSaveFailurePreservesExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "entities.json")
	original := `{"entities":[{"id":1,"canonical":{"from":"A","to":"甲"},"aliases":[]}]}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}

	invalid := model.EntityMapping{
		Entities: []model.Entity{
			{ID: 1, Canonical: model.NamePair{From: "", To: "甲"}},
		},
	}
	if err := Save(path, invalid); err == nil {
		t.Fatal("expected Save() to reject invalid mapping")
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != original {
		t.Fatalf("existing file mutated on failed Save:\ngot:  %s\nwant: %s", string(after), original)
	}
}

func TestImportReviewedSuccessAndIdempotency(t *testing.T) {
	current := model.EntityMapping{
		Entities: []model.Entity{
			{
				ID:        2,
				Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
				Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
			},
		},
	}
	reviewed := model.EntityMapping{
		Entities: []model.Entity{
			{
				ID:        2,
				Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
				Aliases: []model.NamePair{
					{From: "ミナト", To: "湊"},     // idempotent existing alias
					{From: "ミナトちゃん", To: "小湊"}, // new alias for existing entity
				},
			},
			{
				ID:        0,
				Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"},
				Aliases:   []model.NamePair{{From: "アキバ", To: "秋葉原"}},
			},
		},
	}

	merged, err := ImportReviewed(current, reviewed)
	if err != nil {
		t.Fatalf("ImportReviewed() error: %v", err)
	}

	want := model.EntityMapping{
		Entities: []model.Entity{
			{
				ID:        2,
				Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
				Aliases: []model.NamePair{
					{From: "ミナト", To: "湊"},
					{From: "ミナトちゃん", To: "小湊"},
				},
			},
			{
				ID:        3, // maxID (2) + 1
				Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"},
				Aliases:   []model.NamePair{{From: "アキバ", To: "秋葉原"}},
			},
		},
	}
	if !reflect.DeepEqual(merged, want) {
		t.Fatalf("merged mismatch:\ngot  %#v\nwant %#v", merged, want)
	}

	// Verify current was not mutated.
	if len(current.Entities) != 1 || len(current.Entities[0].Aliases) != 1 {
		t.Fatalf("current mapping was mutated: %#v", current)
	}

	// Importing the merged result again should be idempotent.
	reimported, err := ImportReviewed(merged, merged)
	if err != nil {
		t.Fatalf("idempotent re-import error: %v", err)
	}
	if !reflect.DeepEqual(reimported, want) {
		t.Fatalf("idempotent re-import mismatch:\ngot  %#v\nwant %#v", reimported, want)
	}
}

func TestImportReviewedConflictsAreAllOrNothing(t *testing.T) {
	current := model.EntityMapping{
		Entities: []model.Entity{
			{
				ID:        1,
				Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
				Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
			},
			{
				ID:        2,
				Canonical: model.NamePair{From: "朝倉ユイ", To: "朝倉由依"},
				Aliases:   []model.NamePair{},
			},
		},
	}

	cases := []struct {
		name     string
		reviewed model.EntityMapping
	}{
		{
			name: "canonical translation conflict",
			reviewed: model.EntityMapping{
				Entities: []model.Entity{
					{Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"}},
					{ID: 1, Canonical: model.NamePair{From: "新堂ミナト", To: "新堂港"}},
				},
			},
		},
		{
			name: "existing entity ID mismatch",
			reviewed: model.EntityMapping{
				Entities: []model.Entity{
					{ID: 99, Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"}},
				},
			},
		},
		{
			name: "existing alias translation conflict",
			reviewed: model.EntityMapping{
				Entities: []model.Entity{
					{
						ID:        1,
						Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
						Aliases:   []model.NamePair{{From: "ミナト", To: "小湊"}},
					},
				},
			},
		},
		{
			name: "alias conflicts with another entity canonical",
			reviewed: model.EntityMapping{
				Entities: []model.Entity{
					{
						ID:        1,
						Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
						Aliases:   []model.NamePair{{From: "朝倉ユイ", To: "由依"}},
					},
				},
			},
		},
		{
			name: "new canonical conflicts with existing alias",
			reviewed: model.EntityMapping{
				Entities: []model.Entity{
					{Canonical: model.NamePair{From: "ミナト", To: "湊"}},
				},
			},
		},
		{
			name: "new entity carries non-zero ID",
			reviewed: model.EntityMapping{
				Entities: []model.Entity{
					{ID: 10, Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"}},
				},
			},
		},
		{
			name: "duplicate canonical inside reviewed",
			reviewed: model.EntityMapping{
				Entities: []model.Entity{
					{Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"}},
					{Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"}},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ImportReviewed(current, tc.reviewed); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
			// Ensure current stayed untouched even when first item in reviewed was valid.
			if len(current.Entities) != 2 || len(current.Entities[0].Aliases) != 1 {
				t.Fatalf("current mutated on failed import: %#v", current)
			}
		})
	}
}
