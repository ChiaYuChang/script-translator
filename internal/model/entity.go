package model

import "strings"

// NamePair maps a source-language entity name to its translated form.
type NamePair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

func (p NamePair) String() string {
	return p.From + ": " + p.To
}

// Entity describes a canonical name and its alternative forms.
// ID is assigned by the database; a new entity may omit it.
type Entity struct {
	ID        int64      `json:"id,omitzero"`
	Canonical NamePair   `json:"canonical"`
	Aliases   []NamePair `json:"aliases"`
}

// String formats the entity as a bulleted canonical mapping with indented
// alias sub-items.
func (e Entity) String() string {
	var sb strings.Builder
	sb.WriteString("- ")
	sb.WriteString(e.Canonical.String())
	for _, alias := range e.Aliases {
		sb.WriteString("\n  - ")
		sb.WriteString(alias.String())
	}
	return sb.String()
}

// EntityMapping is the content of entities.json.
type EntityMapping struct {
	Entities []Entity `json:"entities"`
}
