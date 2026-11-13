package model

// NamePair maps a source-language entity name to its translated form.
type NamePair struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Entity describes a canonical name and its alternative forms.
// ID is assigned by the database; a new entity may omit it.
type Entity struct {
	ID        int64      `json:"id,omitzero"`
	Canonical NamePair   `json:"canonical"`
	Aliases   []NamePair `json:"aliases"`
}

// EntityMapping is the content of entities.json.
type EntityMapping struct {
	Entities []Entity `json:"entities"`
}
