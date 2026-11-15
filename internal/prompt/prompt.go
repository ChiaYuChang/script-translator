package prompt

import (
	"bytes"
	"embed"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"text/template"

	"go-script-translate/internal/model"
)

const (
	DefaultLanguageFrom     = "Japanese"
	DefaultLanguageTo       = "Traditional Chinese"
	DefaultLanguageLocation = "Taiwan (zh-TW)"
)

//go:embed templates/*.gotmpl
var templateFS embed.FS

var (
	entityRecognitionTmpl = template.Must(template.ParseFS(templateFS, "templates/entity_recognition.gotmpl"))
	translationTmpl       = template.Must(template.ParseFS(templateFS, "templates/translation.gotmpl"))
)

// Language configures source/target languages and optional regional locale
// injected into the entity recognition and translation system prompts.
type Language struct {
	From     string `json:"from,omitzero" jsonschema:"Source script language (default: Japanese)"`
	To       string `json:"to,omitzero" jsonschema:"Target localization language (default: Traditional Chinese)"`
	Location string `json:"location,omitzero" jsonschema:"Optional target region or locale (default: Taiwan (zh-TW) when language is omitted)"`
}

// DefaultLanguage returns the default Japanese -> Traditional Chinese (Taiwan)
// language settings.
func DefaultLanguage() Language {
	return Language{
		From:     DefaultLanguageFrom,
		To:       DefaultLanguageTo,
		Location: DefaultLanguageLocation,
	}
}

// Normalize trims whitespace and fills omitted defaults.
// When all fields are empty, DefaultLanguage is applied. When at least one
// field is set, omitted From/To fall back to defaults while Location is
// preserved as-is so custom target languages do not implicitly inherit a
// Taiwanese locale.
func (l *Language) Normalize() {
	if l == nil {
		return
	}
	l.From = strings.TrimSpace(l.From)
	l.To = strings.TrimSpace(l.To)
	l.Location = strings.TrimSpace(l.Location)
	if *l == (Language{}) {
		*l = DefaultLanguage()
		return
	}
	if l.From == "" {
		l.From = DefaultLanguageFrom
	}
	if l.To == "" {
		l.To = DefaultLanguageTo
	}
}

// TargetStyle returns the formatted target language phrase used in prompts.
func (l Language) TargetStyle() string {
	to := strings.TrimSpace(l.To)
	loc := strings.TrimSpace(l.Location)
	if to == "" {
		to = DefaultLanguageTo
	}
	if loc == "" {
		return to
	}
	return fmt.Sprintf("%s (%s)", to, loc)
}

// ScriptLine represents one Speaker:Text dialogue or narration line for
// prompt examples. An empty Speaker renders as "OS:...".
type ScriptLine struct {
	Speaker string `json:"speaker,omitzero" jsonschema:"Source speaker name; empty for narration (OS)"`
	Text    string `json:"text" jsonschema:"Dialogue or narration text"`
}

// FormatTranscriptLines formats Speaker:Text lines identically to runtime
// transcript and History sections.
func FormatTranscriptLines(lines []ScriptLine) string {
	if len(lines) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, line := range lines {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString(model.TranslatedText{Text: line.Text}.Transcript(line.Speaker))
	}
	return sb.String()
}

// RecognizedEntityExample defines one entity entry in prompt examples
// (omitting database IDs and allowing optional aliases in config input).
type RecognizedEntityExample struct {
	Canonical model.NamePair   `json:"canonical" jsonschema:"Canonical source/target name pair"`
	Aliases   []model.NamePair `json:"aliases,omitzero" jsonschema:"Optional list of recognized aliases"`
}

// FormatEntitiesList renders entity example entries using model.Entity.String().
func FormatEntitiesList(entities []RecognizedEntityExample) string {
	var sb strings.Builder
	for i, e := range entities {
		if i > 0 {
			sb.WriteByte('\n')
		}
		sb.WriteString((model.Entity{
			Canonical: e.Canonical,
			Aliases:   e.Aliases,
		}).String())
	}
	return sb.String()
}

// EntityRecognitionExampleInput defines the structured KnownEntities and
// Transcript sections for an entity recognition prompt example.
type EntityRecognitionExampleInput struct {
	KnownEntities []RecognizedEntityExample `json:"known_entities,omitzero" jsonschema:"Known entities shown in KnownEntities section"`
	Transcript    []ScriptLine              `json:"transcript" jsonschema:"Transcript lines shown in Transcript section"`
}

// FormatKnownEntities formats KnownEntities lines for entity_recognition.gotmpl.
func (in EntityRecognitionExampleInput) FormatKnownEntities() string {
	return FormatEntitiesList(in.KnownEntities)
}

// FormatTranscript formats Transcript lines for entity_recognition.gotmpl.
func (in EntityRecognitionExampleInput) FormatTranscript() string {
	return FormatTranscriptLines(in.Transcript)
}

// EntityRecognitionExampleOutput defines the expected JSON output payload in an
// entity recognition prompt example.
type EntityRecognitionExampleOutput struct {
	Entity []RecognizedEntityExample `json:"entity" jsonschema:"Expected entity list returned by the model"`
}

// FormatJSON renders the expected JSON output for entity_recognition.gotmpl.
func (out EntityRecognitionExampleOutput) FormatJSON() (string, error) {
	items := make([]RecognizedEntityExample, len(out.Entity))
	for i, e := range out.Entity {
		aliases := e.Aliases
		if aliases == nil {
			aliases = []model.NamePair{}
		}
		items[i] = RecognizedEntityExample{
			Canonical: e.Canonical,
			Aliases:   aliases,
		}
	}
	return marshalIndented(struct {
		Entity []RecognizedEntityExample `json:"entity"`
	}{
		Entity: items,
	})
}

// EntityRecognitionExample configures the few-shot example embedded in the
// entity recognition system prompt.
type EntityRecognitionExample struct {
	Input       EntityRecognitionExampleInput  `json:"input" jsonschema:"Structured example input"`
	Output      EntityRecognitionExampleOutput `json:"output" jsonschema:"Expected example JSON output"`
	Explanation []string                       `json:"explanation,omitzero" jsonschema:"Optional bullet explanations following EXAMPLE OUTPUT"`
}

// Validate verifies that a configured entity recognition example has a valid
// transcript and non-empty canonical/alias pairs.
func (e EntityRecognitionExample) Validate() error {
	if len(e.Input.Transcript) == 0 {
		return errors.New("entity recognition example input.transcript must not be empty")
	}
	for i, line := range e.Input.Transcript {
		if strings.TrimSpace(line.Text) == "" {
			return fmt.Errorf("entity recognition example input.transcript[%d].text must not be empty", i)
		}
	}
	for i, ent := range e.Output.Entity {
		if strings.TrimSpace(ent.Canonical.From) == "" || strings.TrimSpace(ent.Canonical.To) == "" {
			return fmt.Errorf("entity recognition example output.entity[%d].canonical must have non-empty from and to", i)
		}
		for j, alias := range ent.Aliases {
			if strings.TrimSpace(alias.From) == "" || strings.TrimSpace(alias.To) == "" {
				return fmt.Errorf("entity recognition example output.entity[%d].aliases[%d] must have non-empty from and to", i, j)
			}
		}
	}
	return nil
}

// DefaultEntityRecognitionExample returns the default Japanese -> Traditional
// Chinese entity recognition example.
func DefaultEntityRecognitionExample() EntityRecognitionExample {
	return EntityRecognitionExample{
		Input: EntityRecognitionExampleInput{
			KnownEntities: []RecognizedEntityExample{
				{
					Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
					Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
				},
				{
					Canonical: model.NamePair{From: "朝倉ユイ", To: "朝倉由依"},
				},
			},
			Transcript: []ScriptLine{
				{Speaker: "朝倉ユイ", Text: "ミナトちゃん、秋葉原へ行こう。"},
				{Speaker: "新堂ミナト", Text: "いいね。"},
			},
		},
		Output: EntityRecognitionExampleOutput{
			Entity: []RecognizedEntityExample{
				{
					Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
					Aliases:   []model.NamePair{{From: "ミナトちゃん", To: "小湊"}},
				},
				{
					Canonical: model.NamePair{From: "秋葉原", To: "秋葉原"},
					Aliases:   []model.NamePair{},
				},
			},
		},
		Explanation: []string{
			`"新堂ミナト" is already known, so its existing canonical translation is preserved.`,
			`"ミナトちゃん" appears in the transcript and is recognized as a new alias of the existing character.`,
			`"秋葉原" is a newly recognized location, so it is returned as a new entity.`,
			`"朝倉ユイ" has no new aliases and is therefore omitted.`,
			`All example entities are illustrative; do not include them unless they are found in the actual input.`,
		},
	}
}

// TranslateUnitExample matches the wire JSON shape sent to the model inside
// Input: (with field "name" for speaker).
type TranslateUnitExample struct {
	Index   int    `json:"index" jsonschema:"Dialogue unit index"`
	Speaker string `json:"name,omitzero" jsonschema:"Source speaker name; empty for narration"`
	Text    string `json:"text" jsonschema:"Source dialogue or narration text"`
}

// TranslationExampleInput defines the structured Entries, History, and Input
// sections for a translation prompt example.
type TranslationExampleInput struct {
	Entries  []RecognizedEntityExample `json:"entries,omitzero" jsonschema:"Entity mappings shown in Entries section"`
	History  []ScriptLine              `json:"history,omitzero" jsonschema:"Optional prior translated lines shown in History section"`
	Dialogue []TranslateUnitExample    `json:"dialogue" jsonschema:"Source dialogue units shown in Input JSON array"`
}

// FormatEntries formats Entries lines for translation.gotmpl.
func (in TranslationExampleInput) FormatEntries() string {
	return FormatEntitiesList(in.Entries)
}

// HasHistory reports whether History lines are configured.
func (in TranslationExampleInput) HasHistory() bool {
	return len(in.History) > 0
}

// FormatHistory formats History lines for translation.gotmpl.
func (in TranslationExampleInput) FormatHistory() string {
	return FormatTranscriptLines(in.History)
}

// FormatDialogue renders the Input JSON array for translation.gotmpl.
func (in TranslationExampleInput) FormatDialogue() (string, error) {
	// Always serialize "name" (including "") to match runtime TranslateUnit wire JSON.
	type wireUnit struct {
		Index   int    `json:"index"`
		Speaker string `json:"name"`
		Text    string `json:"text"`
	}
	items := make([]wireUnit, len(in.Dialogue))
	for i, u := range in.Dialogue {
		items[i] = wireUnit(u)
	}
	return marshalIndented(items)
}

// TranslationExampleOutput defines the expected JSON output payload in a
// translation prompt example.
type TranslationExampleOutput struct {
	Dialogue []model.TranslatedText `json:"dialogue" jsonschema:"Expected translated dialogue units"`
}

// FormatJSON renders the expected JSON output for translation.gotmpl.
func (out TranslationExampleOutput) FormatJSON() (string, error) {
	items := out.Dialogue
	if items == nil {
		items = []model.TranslatedText{}
	}
	return marshalIndented(struct {
		Dialogue []model.TranslatedText `json:"dialogue"`
	}{
		Dialogue: items,
	})
}

// TranslationExample configures the few-shot example embedded in the
// translation system prompt.
type TranslationExample struct {
	Input  TranslationExampleInput  `json:"input" jsonschema:"Structured example input"`
	Output TranslationExampleOutput `json:"output" jsonschema:"Expected example JSON output"`
	Note   string                   `json:"note,omitzero" jsonschema:"Optional explanatory note rendered after EXAMPLE OUTPUT"`
}

// Validate checks that a configured translation example has aligned input and
// output indices and non-empty text.
func (e TranslationExample) Validate() error {
	if len(e.Input.Dialogue) == 0 {
		return errors.New("translation example input.dialogue must not be empty")
	}
	if len(e.Output.Dialogue) != len(e.Input.Dialogue) {
		return fmt.Errorf("translation example output.dialogue length %d must match input.dialogue length %d", len(e.Output.Dialogue), len(e.Input.Dialogue))
	}
	for i, inUnit := range e.Input.Dialogue {
		if strings.TrimSpace(inUnit.Text) == "" {
			return fmt.Errorf("translation example input.dialogue[%d].text must not be empty", i)
		}
		outUnit := e.Output.Dialogue[i]
		if outUnit.Index != inUnit.Index {
			return fmt.Errorf("translation example output.dialogue[%d].index = %d, want %d", i, outUnit.Index, inUnit.Index)
		}
		if strings.TrimSpace(outUnit.Text) == "" {
			return fmt.Errorf("translation example output.dialogue[%d].text must not be empty", i)
		}
	}
	return nil
}

// DefaultTranslationExample returns the default Japanese -> Traditional Chinese
// translation example.
func DefaultTranslationExample() TranslationExample {
	return TranslationExample{
		Input: TranslationExampleInput{
			Entries: []RecognizedEntityExample{
				{
					Canonical: model.NamePair{From: "新堂ミナト", To: "新堂湊"},
					Aliases:   []model.NamePair{{From: "ミナト", To: "湊"}},
				},
				{
					Canonical: model.NamePair{From: "朝倉ユイ", To: "朝倉由依"},
				},
			},
			History: []ScriptLine{
				{Speaker: "朝倉ユイ", Text: "「早安！今天天氣真好。」"},
			},
			Dialogue: []TranslateUnitExample{
				{Index: 0, Speaker: "新堂ミナト", Text: "「おはよう、ユイ。」"},
				{Index: 1, Speaker: "新堂ミナト", Text: "（今日は誘ってみようかな……）"},
				{Index: 2, Speaker: "", Text: "午後三時。放課後の教室には、二人だけが残っていた。"},
			},
		},
		Output: TranslationExampleOutput{
			Dialogue: []model.TranslatedText{
				{Index: 0, Text: "「早安，由依。」"},
				{Index: 1, Text: "（今天試著約她看看好了……）"},
				{Index: 2, Text: "下午三點。放學後的教室裡，只剩下他們兩人。"},
			},
		},
		Note: `The final dialogue unit demonstrates narration: the source "name" is empty, and no speaker field is returned.`,
	}
}

// RenderEntityRecognition renders the entity recognition system prompt using
// language settings and example (falling back to defaults when omitted).
func RenderEntityRecognition(lang Language, example *EntityRecognitionExample) (string, error) {
	lang.Normalize()
	ex := DefaultEntityRecognitionExample()
	if example != nil {
		if err := example.Validate(); err != nil {
			return "", fmt.Errorf("prompt: %w", err)
		}
		ex = *example
	}
	return renderTemplate(entityRecognitionTmpl, struct {
		Language Language
		Example  EntityRecognitionExample
	}{
		Language: lang,
		Example:  ex,
	})
}

// RenderTranslation renders the translation system prompt using language
// settings and example (falling back to defaults when omitted).
func RenderTranslation(lang Language, example *TranslationExample) (string, error) {
	lang.Normalize()
	ex := DefaultTranslationExample()
	if example != nil {
		if err := example.Validate(); err != nil {
			return "", fmt.Errorf("prompt: %w", err)
		}
		ex = *example
	}
	return renderTemplate(translationTmpl, struct {
		Language Language
		Example  TranslationExample
	}{
		Language: lang,
		Example:  ex,
	})
}

func renderTemplate(tmpl *template.Template, data any) (string, error) {
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("prompt: execute %s: %w", tmpl.Name(), err)
	}
	return buf.String(), nil
}

func marshalIndented(v any) (string, error) {
	var buf bytes.Buffer
	if err := json.MarshalWrite(&buf, v, jsontext.WithIndent("  ")); err != nil {
		return "", err
	}
	return buf.String(), nil
}
