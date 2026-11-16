package llm

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"

	"go-script-translate/internal/model"
	"go-script-translate/internal/observation"
	"go-script-translate/internal/prompt"
)

// TranslationPrompt is the default system prompt rendered from internal/prompt.
var TranslationPrompt = mustRenderDefaultTranslationPrompt()

func mustRenderDefaultTranslationPrompt() string {
	s, err := prompt.RenderTranslation(prompt.DefaultLanguage(), nil)
	if err != nil {
		panic(err)
	}
	return s
}

type translateInputUnit struct {
	Index   int    `json:"index"`
	Speaker string `json:"name"`
	Text    string `json:"text"`
}

// translationOutput wraps the batch output array because Strict Structured
// Outputs require a JSON object at the schema root.
type translationOutput struct {
	Dialogue model.TranslateBatchOutput `json:"dialogue"`
}

var translationSchema = MustSchema[translationOutput]()

// TranslateParams configures a Translate call. Defaults belong in config validation;
// Translate rejects non-positive BatchSize/MaxAttempt and empty Model.
type TranslateParams struct {
	Model           string                             `json:"model" jsonschema:"Model name for script translation"`
	BatchSize       int                                `json:"batch_size" jsonschema:"Number of dialogue units per translation batch"`
	MaxAttempt      int                                `json:"max_attempt" jsonschema:"Maximum attempts per translation batch"`
	Example         *prompt.TranslationExample         `json:"example,omitzero" jsonschema:"Optional custom few-shot example for translation"`
	Language        prompt.Language                    `json:"-"`
	ResumeFrom      []model.TranslatedText             `json:"-"`
	OnBatchComplete func([]model.TranslatedText) error `json:"-"`
}

// TotalBatches returns the number of sequential translation batches needed for
// unitCount dialogue units at the given batchSize.
func TotalBatches(unitCount, batchSize int) int {
	if unitCount <= 0 || batchSize <= 0 {
		return 0
	}
	return (unitCount + batchSize - 1) / batchSize
}

// Translate translates dialogue units sequentially in batches of param.BatchSize,
// passing Entities and a compact transcript of previously translated lines per batch.
func (p *Provider) Translate(
	ctx context.Context,
	req model.TranslateRequest,
	param TranslateParams,
) (model.TranslateResponse, error) {
	if p == nil || p.client == nil {
		return nil, errors.New("translation: nil client")
	}
	if strings.TrimSpace(param.Model) == "" {
		return nil, errors.New("translation: empty model name")
	}
	if param.BatchSize <= 0 {
		return nil, fmt.Errorf("translation: invalid batch size %d", param.BatchSize)
	}
	if param.MaxAttempt <= 0 {
		return nil, fmt.Errorf("translation: invalid max attempt %d", param.MaxAttempt)
	}

	instructions, err := prompt.RenderTranslation(param.Language, param.Example)
	if err != nil {
		return nil, fmt.Errorf("translation: render prompt: %w", err)
	}

	if len(req.Dialogue) == 0 {
		if len(param.ResumeFrom) > 0 {
			return nil, fmt.Errorf("translation: resume count %d exceeds dialogue units 0", len(param.ResumeFrom))
		}
		return model.TranslateResponse{}, nil
	}
	if err := validateResumePrefix(req.Dialogue, param.ResumeFrom); err != nil {
		return nil, err
	}

	translated := make(model.TranslateResponse, len(param.ResumeFrom), len(req.Dialogue))
	copy(translated, param.ResumeFrom)
	startOffset := len(param.ResumeFrom)
	if startOffset == len(req.Dialogue) {
		return translated, nil
	}

	schema, err := ToSchemaMap(translationSchema)
	if err != nil {
		return nil, fmt.Errorf("translation: schema map: %w", err)
	}

	resolved, err := translationSchema.Resolve(nil)
	if err != nil {
		return nil, fmt.Errorf("translation: resolve schema: %w", err)
	}

	entriesSection := formatEntitiesSection(req.Entity)

	speaker := make([]string, len(req.Dialogue))
	for i := range speaker {
		speaker[i] = req.Dialogue[i].Speaker
	}

	currentBatchSize := param.BatchSize
	buildBatchPrompt := func(start, batchNum int) (int, int, string, error) {
		end := min(start+currentBatchSize, len(req.Dialogue))
		totalBatches := (batchNum - 1) + TotalBatches(len(req.Dialogue)-start, currentBatchSize)

		wireUnits := make([]translateInputUnit, end-start)
		for i, u := range req.Dialogue[start:end] {
			wireUnits[i] = translateInputUnit{
				Index:   u.Index,
				Speaker: u.Speaker,
				Text:    u.Text,
			}
		}
		batchInput, err := json.Marshal(wireUnits)
		if err != nil {
			return 0, 0, "", fmt.Errorf("translation: batch %d/%d: marshal input: %w", batchNum, totalBatches, err)
		}
		return end, totalBatches, formatTranslationQuery(entriesSection, speaker, translated, batchInput), nil
	}

	for start, batchNum := startOffset, 1; start < len(req.Dialogue); batchNum++ {
		end, totalBatches, batchPrompt, err := buildBatchPrompt(start, batchNum)
		if err != nil {
			return nil, err
		}

		var batchResult []model.TranslatedText
		var lastErr error
		for attempt := 0; attempt < param.MaxAttempt; attempt++ {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("translation: batch %d/%d: %w", batchNum, totalBatches, err)
			}
			if attempt == 0 {
				if err := p.paceCall(ctx); err != nil {
					return nil, fmt.Errorf("translation: batch %d/%d: %w", batchNum, totalBatches, err)
				}
			}

			batchResult, lastErr = p.doTranslateBatchOnce(ctx, param.Model, instructions, schema, resolved, batchPrompt, req.Dialogue[start:end], batchNum, totalBatches, currentBatchSize, attempt+1)
			if lastErr == nil {
				break
			}
			if errors.Is(lastErr, context.Canceled) || errors.Is(lastErr, context.DeadlineExceeded) {
				return nil, fmt.Errorf("translation: batch %d/%d: %w", batchNum, totalBatches, lastErr)
			}

			batchLen := end - start
			var delay time.Duration
			var backoffApplied bool
			var nextBatchSize int
			var nextTotalBatches int
			if attempt+1 < param.MaxAttempt {
				delay, backoffApplied = p.retryDelay(lastErr, attempt)
				if isUnexpectedEOFError(lastErr) && batchLen > 1 {
					nextBatchSize = max(batchLen*9/10, 1)
					nextTotalBatches = (batchNum - 1) + TotalBatches(len(req.Dialogue)-start, nextBatchSize)
				}
			}
			attrs := []any{
				slog.Int("batch", batchNum),
				slog.Int("total_batch", totalBatches),
				slog.Int("batch_size", currentBatchSize),
				slog.Int("attempt", attempt+1),
				slog.Int("max_attempt", param.MaxAttempt),
				slog.Any("error", lastErr),
			}
			if delay > 0 {
				attrs = append(attrs, slog.Duration("wait", delay), slog.Bool("backoff", backoffApplied))
			}
			if nextBatchSize > 0 && nextBatchSize < batchLen {
				attrs = append(attrs,
					slog.Int("next_batch_size", nextBatchSize),
					slog.Int("next_total_batch", nextTotalBatches),
				)
			}
			p.log().WarnContext(ctx, "translation batch attempt failed", attrs...)
			if nextBatchSize > 0 && nextBatchSize < batchLen {
				currentBatchSize = nextBatchSize
				end, totalBatches, batchPrompt, err = buildBatchPrompt(start, batchNum)
				if err != nil {
					return nil, err
				}
			}
			if delay > 0 {
				if err := p.sleep(ctx, delay); err != nil {
					return nil, fmt.Errorf("translation: batch %d/%d: %w", batchNum, totalBatches, err)
				}
			}
		}
		if lastErr != nil {
			return nil, fmt.Errorf("translation: batch %d/%d failed after %d attempts: %w", batchNum, totalBatches, param.MaxAttempt, lastErr)
		}

		translated = append(translated, batchResult...)
		start += len(batchResult)
		if param.OnBatchComplete != nil {
			if err := param.OnBatchComplete(slices.Clone(translated)); err != nil {
				return nil, fmt.Errorf("translation: persist batch %d/%d checkpoint: %w", batchNum, totalBatches, err)
			}
		}
	}

	return translated, nil
}

func validateResumePrefix(dialogue []model.TranslateUnit, resume []model.TranslatedText) error {
	if len(resume) > len(dialogue) {
		return fmt.Errorf("translation: resume count %d exceeds dialogue units %d", len(resume), len(dialogue))
	}
	for i, item := range resume {
		if item.Index != dialogue[i].Index {
			return fmt.Errorf("translation: resume unit %d has index %d, want %d", i, item.Index, dialogue[i].Index)
		}
		if strings.TrimSpace(item.Text) == "" {
			return fmt.Errorf("translation: resume index %d has blank text", item.Index)
		}
		if err := validateMatchingOuterDelimiters(dialogue[i].Text, item.Text); err != nil {
			return fmt.Errorf("translation: resume index %d: %w", item.Index, err)
		}
	}
	return nil
}

func formatEntitiesSection(entities []model.Entity) string {
	var sb strings.Builder
	sb.WriteString("Entries:\n")
	for _, e := range entities {
		sb.WriteString(e.String())
		sb.WriteByte('\n')
	}
	return sb.String()
}

func formatTranslationQuery(
	entriesSection string,
	speaker []string,
	translated []model.TranslatedText,
	inputJSON []byte,
) string {
	var sb strings.Builder
	sb.WriteString(entriesSection)

	if len(translated) > 0 {
		sb.WriteString("\nHistory:\n")
		for i, t := range translated {
			if i > 0 {
				sb.WriteByte('\n')
			}
			// translated grows 1:1 with source in batch order.
			sb.WriteString(t.Transcript(speaker[i]))
		}
		sb.WriteByte('\n')
	}

	sb.WriteString("\nInput:\n")
	sb.Write(inputJSON)
	return sb.String()
}

func (p *Provider) doTranslateBatchOnce(
	ctx context.Context,
	modelName string,
	instructions string,
	schemaMap map[string]any,
	resolved *jsonschema.Resolved,
	userPrompt string,
	batch []model.TranslateUnit,
	batchNum int,
	totalBatch int,
	batchSize int,
	attempt int,
) (result []model.TranslatedText, err error) {
	started := time.Now()
	var usage observation.TokenUsage
	defer func() {
		p.recordAttempt(ctx, observation.MetricTypeTranslationBatchUsage, modelName, batchNum, totalBatch, batchSize, attempt, started, usage, err)
	}()

	resp, err := p.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:        modelName,
		Instructions: openai.String(instructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(userPrompt),
		},
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:   "script_translation",
					Schema: schemaMap,
					Strict: openai.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("request: %w", err)
	}
	usage = extractUsage(resp)

	outputText, err := finalJSONText(resp)
	if err != nil {
		return nil, err
	}

	return parseAndValidateTranslationBatch(resolved, outputText, batch)
}

func parseAndValidateTranslationBatch(
	resolved *jsonschema.Resolved,
	outputText string,
	batch []model.TranslateUnit,
) ([]model.TranslatedText, error) {
	output, err := DecodeJSON[translationOutput](resolved, outputText)
	if err != nil {
		return nil, err
	}

	expected := make(map[int]struct{}, len(batch))
	for _, unit := range batch {
		expected[unit.Index] = struct{}{}
	}

	byIndex := make(map[int]model.TranslatedText, len(batch))
	for _, item := range output.Dialogue {
		if _, ok := expected[item.Index]; !ok {
			return nil, fmt.Errorf("unknown index %d", item.Index)
		}
		if _, dup := byIndex[item.Index]; dup {
			return nil, fmt.Errorf("duplicate index %d", item.Index)
		}
		if strings.TrimSpace(item.Text) == "" {
			return nil, fmt.Errorf("index %d: empty text", item.Index)
		}
		byIndex[item.Index] = model.TranslatedText{
			Index: item.Index,
			Text:  item.Text,
		}
	}

	ordered := make([]model.TranslatedText, 0, len(batch))
	for _, unit := range batch {
		item, ok := byIndex[unit.Index]
		if !ok {
			return nil, fmt.Errorf("missing index %d", unit.Index)
		}
		if err := validateMatchingOuterDelimiters(unit.Text, item.Text); err != nil {
			return nil, fmt.Errorf("index %d: %w", unit.Index, err)
		}
		ordered = append(ordered, item)
	}
	return ordered, nil
}

var outerDialogueDelimiters = map[rune]rune{
	'（': '）',
	'「': '」',
	'『': '』',
}

func validateMatchingOuterDelimiters(source, translated string) error {
	trimmedSrc := strings.TrimSpace(source)
	trimmedDst := strings.TrimSpace(translated)
	openRune, _ := utf8.DecodeRuneInString(trimmedSrc)
	closeRune, ok := outerDialogueDelimiters[openRune]
	if !ok {
		return nil
	}
	lastRune, _ := utf8.DecodeLastRuneInString(trimmedSrc)
	if lastRune != closeRune {
		return nil
	}

	openMark, closeMark := string(openRune), string(closeRune)
	if !strings.HasPrefix(trimmedDst, openMark) || !strings.HasSuffix(trimmedDst, closeMark) {
		return fmt.Errorf("must preserve outer %s%s wrapper", openMark, closeMark)
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(trimmedDst, openMark), closeMark)
	if strings.TrimSpace(inner) == "" {
		return fmt.Errorf("empty content inside outer %s%s", openMark, closeMark)
	}
	return nil
}
