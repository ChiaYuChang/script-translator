package llm

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/jsonschema-go/jsonschema"

	"go-script-translate/internal/model"
	"go-script-translate/internal/observation"
	"go-script-translate/internal/prompt"
)

// recognitionOutput is deliberately distinct from model.Entity: the model
// never assigns database IDs. A known entity's canonical name identifies it.
type recognitionOutput struct {
	Entity []recognizedEntity `json:"entity"`
}

type recognizedEntity struct {
	Canonical model.NamePair   `json:"canonical"`
	Aliases   []model.NamePair `json:"aliases"`
}

// Strict Structured Outputs require an object root, required fields, and
// additionalProperties: false on every object.
var recognitionSchema = MustSchema[recognitionOutput]()

// EntityRecognitionPrompt is the default system prompt rendered from internal/prompt.
var EntityRecognitionPrompt = mustRenderDefaultEntityRecognitionPrompt()

func mustRenderDefaultEntityRecognitionPrompt() string {
	s, err := prompt.RenderEntityRecognition(prompt.DefaultLanguage(), nil)
	if err != nil {
		panic(err)
	}
	return s
}

// RecognizeEntitiesParams configures a RecognizeEntities call.
type RecognizeEntitiesParams struct {
	Provider    string                           `json:"provider,omitzero" jsonschema:"Provider key in top-level providers map (defaults to \"default\")"`
	Model       string                           `json:"model" jsonschema:"Model name for entity recognition"`
	Dictionary  []model.Entity                   `json:"-"`
	Language    prompt.Language                  `json:"-"`
	MaxAttempt  int                              `json:"max_attempt" jsonschema:"Maximum attempts per entity recognition call"`
	ExampleFile string                           `json:"example_file,omitzero" jsonschema:"Optional path to custom entity recognition example JSON file (supports ~ expansion)"`
	Example     *prompt.EntityRecognitionExample `json:"-"`
}

// RecognizeEntities performs a stateless Responses API request. It returns
// matched known mappings and new candidates for entities.json; no DB writes.
func (p *Provider) RecognizeEntities(
	ctx context.Context,
	transcript string,
	param RecognizeEntitiesParams,
) ([]model.Entity, error) {
	if p == nil || p.client == nil {
		return nil, errors.New("entity recognition: nil client")
	}
	if strings.TrimSpace(param.Model) == "" {
		return nil, errors.New("entity recognition: empty model name")
	}
	if param.MaxAttempt <= 0 {
		return nil, fmt.Errorf("entity recognition: invalid max attempt %d", param.MaxAttempt)
	}
	if strings.TrimSpace(transcript) == "" {
		return nil, errors.New("entity recognition: empty transcript")
	}

	schema, err := ToSchemaMap(recognitionSchema)
	if err != nil {
		return nil, fmt.Errorf("entity recognition: schema map: %w", err)
	}

	resolved, err := recognitionSchema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("entity recognition: resolve schema: %w", err)
	}

	instructions, err := prompt.RenderEntityRecognition(param.Language, param.Example)
	if err != nil {
		return nil, fmt.Errorf("entity recognition: render prompt: %w", err)
	}

	known := MatchKnownEntities(transcript, param.Dictionary)
	userPrompt := formatRecognitionQuery(known, transcript)

	var proposals []recognizedEntity
	var lastErr error
	for attempt := 0; attempt < param.MaxAttempt; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("entity recognition: %w", err)
		}
		if attempt == 0 {
			if err := p.paceCall(ctx); err != nil {
				return nil, fmt.Errorf("entity recognition: %w", err)
			}
		}

		proposals, lastErr = p.doRecognizeEntitiesOnce(ctx, param.Model, instructions, schema, resolved, userPrompt, attempt+1)
		if lastErr == nil {
			break
		}
		if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
			return nil, fmt.Errorf("entity recognition: %w", lastErr)
		}

		var delay time.Duration
		var backoffApplied bool
		if attempt+1 < param.MaxAttempt {
			delay, backoffApplied = p.retryDelay(lastErr, attempt)
		}
		attrs := []any{
			slog.Int("attempt", attempt+1),
			slog.Int("max_attempt", param.MaxAttempt),
			slog.Any("error", lastErr),
		}
		if delay > 0 {
			attrs = append(attrs, slog.Duration("wait", delay), slog.Bool("backoff", backoffApplied))
		}
		p.log().WarnContext(ctx, "entity recognition attempt failed", attrs...)
		if delay > 0 {
			if err := p.sleep(ctx, delay); err != nil {
				return nil, fmt.Errorf("entity recognition: %w", err)
			}
		}
	}
	if lastErr != nil {
		return nil, fmt.Errorf("entity recognition failed after %d attempts: %w", param.MaxAttempt, lastErr)
	}

	return mergeRecognizedEntities(transcript, known, param.Dictionary, proposals), nil
}

func (p *Provider) doRecognizeEntitiesOnce(
	ctx context.Context,
	modelName string,
	instructions string,
	schemaMap map[string]any,
	resolved *jsonschema.Resolved,
	requestBody string,
	attempt int,
) (proposals []recognizedEntity, err error) {
	started := time.Now()
	var usage observation.TokenUsage
	defer func() {
		p.recordAttempt(ctx, observation.MetricTypeEntityUsage, modelName, 0, 0, 0, attempt, started, usage, err)
	}()

	outputText, callUsage, err := p.requestStructuredJSON(
		ctx,
		modelName,
		instructions,
		requestBody,
		"entity_recognition",
		schemaMap,
	)
	usage = callUsage
	if err != nil {
		return nil, err
	}
	output, err := DecodeJSON[recognitionOutput](resolved, outputText)
	if err != nil {
		return nil, err
	}
	return output.Entity, nil
}

func formatRecognitionQuery(known []model.Entity, transcript string) string {
	var sb strings.Builder
	sb.WriteString("KnownEntities:\n")
	for _, e := range known {
		sb.WriteString(e.String())
		sb.WriteByte('\n')
	}
	sb.WriteString("\nTranscript:\n")
	sb.WriteString(transcript)
	return sb.String()
}

// MatchKnownEntities collects canonical/alias substring matches, retaining
// dictionary order. One entity can match multiple strings but is emitted once.
func MatchKnownEntities(transcript string, dictionary []model.Entity) []model.Entity {
	matched := make([]model.Entity, 0)
	for _, e := range dictionary {
		found := e.Canonical.From != "" && strings.Contains(transcript, e.Canonical.From)
		if !found {
			for _, alias := range e.Aliases {
				if alias.From != "" && strings.Contains(transcript, alias.From) {
					found = true
					break
				}
			}
		}
		if found {
			matched = append(matched, e)
		}
	}
	return matched
}

// mergeRecognizedEntities keeps DB translations authoritative and only adds
// names which appear in the transcript. It never mutates dictionary slices.
// Model candidates are for human review before import into the mapping DB.
func mergeRecognizedEntities(
	transcript string,
	known []model.Entity,
	dictionary []model.Entity,
	proposals []recognizedEntity,
) []model.Entity {
	result := make([]model.Entity, 0, len(known)+len(proposals))
	index := make(map[string]int)
	stored := make(map[string]model.Entity)
	owner := make(map[string]string) // JP canonical/alias -> canonical JP

	for _, e := range dictionary {
		if e.Canonical.From == "" {
			continue
		}
		stored[e.Canonical.From] = e
		owner[e.Canonical.From] = e.Canonical.From
		for _, a := range e.Aliases {
			if a.From != "" {
				owner[a.From] = e.Canonical.From
			}
		}
	}
	for _, e := range known {
		clone := e
		clone.Aliases = append([]model.NamePair(nil), e.Aliases...)
		index[e.Canonical.From] = len(result)
		result = append(result, clone)
	}

	for _, proposal := range proposals {
		from := strings.TrimSpace(proposal.Canonical.From)
		to := strings.TrimSpace(proposal.Canonical.To)
		if from == "" {
			continue
		}
		// An existing alias cannot become a new independent entity.
		if canonical, ok := owner[from]; ok && canonical != from {
			continue
		}

		idx, exists := index[from]
		if !exists {
			if e, ok := stored[from]; ok {
				clone := e
				clone.Aliases = append([]model.NamePair(nil), e.Aliases...)
				idx = len(result)
				index[from] = idx
				result = append(result, clone)
			} else {
				// New canonical names must appear verbatim, not only by alias.
				if to == "" || !strings.Contains(transcript, from) {
					continue
				}
				idx = len(result)
				index[from] = idx
				result = append(result, model.Entity{
					Canonical: model.NamePair{From: from, To: to},
				})
				owner[from] = from
			}
		}

		for _, alias := range proposal.Aliases {
			name := strings.TrimSpace(alias.From)
			translation := strings.TrimSpace(alias.To)
			if name == "" || translation == "" || name == from || !strings.Contains(transcript, name) {
				continue
			}
			// Prevent an alias from being assigned to two distinct entities.
			if canonical, taken := owner[name]; taken {
				if canonical != from {
					continue
				}
				// Already present as a known alias; don't replace its translation.
				continue
			}
			result[idx].Aliases = append(result[idx].Aliases, model.NamePair{
				From: name, To: translation,
			})
			owner[name] = from
		}
	}
	return result
}
