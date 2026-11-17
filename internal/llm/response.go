package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/responses"
	"github.com/openai/openai-go/v3/shared"

	"go-script-translate/internal/observation"
)

var errResponseTruncated = errors.New("truncated response")

func (p *Provider) requestStructuredJSON(
	ctx context.Context,
	modelName string,
	instructions string,
	userInput string,
	schemaName string,
	schemaMap map[string]any,
) (string, observation.TokenUsage, error) {
	switch p.endpoint {
	case "", EndpointResponses:
		return p.requestViaResponses(ctx, modelName, instructions, userInput, schemaName, schemaMap)
	case EndpointChatCompletions:
		return p.requestViaChatCompletions(ctx, modelName, instructions, userInput, schemaName, schemaMap)
	default:
		return "", observation.TokenUsage{}, fmt.Errorf("unsupported endpoint %q", p.endpoint)
	}
}

func (p *Provider) requestViaResponses(
	ctx context.Context,
	modelName string,
	instructions string,
	userInput string,
	schemaName string,
	schemaMap map[string]any,
) (string, observation.TokenUsage, error) {
	resp, err := p.client.Responses.New(ctx, responses.ResponseNewParams{
		Model:        modelName,
		Instructions: openai.String(instructions),
		Input: responses.ResponseNewParamsInputUnion{
			OfString: openai.String(userInput),
		},
		Text: responses.ResponseTextConfigParam{
			Format: responses.ResponseFormatTextConfigUnionParam{
				OfJSONSchema: &responses.ResponseFormatTextJSONSchemaConfigParam{
					Name:   schemaName,
					Schema: schemaMap,
					Strict: openai.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return "", observation.TokenUsage{}, fmt.Errorf("request: %w", err)
	}
	usage := extractUsage(resp)
	text, err := finalJSONText(resp)
	return text, usage, err
}

func (p *Provider) requestViaChatCompletions(
	ctx context.Context,
	modelName string,
	instructions string,
	userInput string,
	schemaName string,
	schemaMap map[string]any,
) (string, observation.TokenUsage, error) {
	completion, err := p.client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: modelName,
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage(instructions),
			openai.UserMessage(userInput),
		},
		ResponseFormat: openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   schemaName,
					Schema: schemaMap,
					Strict: openai.Bool(true),
				},
			},
		},
	})
	if err != nil {
		return "", observation.TokenUsage{}, fmt.Errorf("request: %w", err)
	}
	usage := extractChatUsage(completion)
	text, err := finalChatCompletionJSONText(completion)
	return text, usage, err
}

func extractUsage(response *responses.Response) observation.TokenUsage {
	if response == nil {
		return observation.TokenUsage{}
	}
	return observation.TokenUsage{
		InputTokens:      response.Usage.InputTokens,
		CachedTokens:     response.Usage.InputTokensDetails.CachedTokens,
		CacheWriteTokens: response.Usage.InputTokensDetails.CacheWriteTokens,
		OutputTokens:     response.Usage.OutputTokens,
		ReasoningTokens:  response.Usage.OutputTokensDetails.ReasoningTokens,
		TotalTokens:      response.Usage.TotalTokens,
	}
}

func extractChatUsage(completion *openai.ChatCompletion) observation.TokenUsage {
	if completion == nil {
		return observation.TokenUsage{}
	}
	return observation.TokenUsage{
		InputTokens:     completion.Usage.PromptTokens,
		CachedTokens:    completion.Usage.PromptTokensDetails.CachedTokens,
		OutputTokens:    completion.Usage.CompletionTokens,
		ReasoningTokens: completion.Usage.CompletionTokensDetails.ReasoningTokens,
		TotalTokens:     completion.Usage.TotalTokens,
	}
}

// finalJSONText extracts the completed final response, excluding reasoning
// and handling refusals explicitly instead of blindly calling OutputText().
func finalJSONText(response *responses.Response) (string, error) {
	if response == nil {
		return "", errors.New("nil response")
	}
	if response.Status == responses.ResponseStatusIncomplete {
		return "", fmt.Errorf("response did not complete: %s: %w", response.Status, errResponseTruncated)
	}
	if response.Status != responses.ResponseStatusCompleted {
		return "", fmt.Errorf("response did not complete: %s", response.Status)
	}

	var text *string
	for _, item := range response.Output {
		if item.Type != "message" || (item.Phase != "" && item.Phase != responses.ResponseOutputMessagePhaseFinalAnswer) {
			continue
		}
		if item.Status != "" && item.Status != "completed" {
			return "", errors.New("final output message did not complete")
		}
		for _, part := range item.Content {
			switch part.Type {
			case "refusal":
				return "", errors.New("model refused the request")
			case "output_text":
				if text != nil {
					return "", errors.New("expected one final JSON text part")
				}
				s := part.Text
				text = &s
			}
		}
	}
	if text == nil || strings.TrimSpace(*text) == "" {
		return "", errors.New("response has no final JSON text")
	}
	return *text, nil
}

func finalChatCompletionJSONText(completion *openai.ChatCompletion) (string, error) {
	if completion == nil {
		return "", errors.New("nil chat completion")
	}
	if len(completion.Choices) == 0 {
		return "", errors.New("chat completion has no choices")
	}
	choice := completion.Choices[0]
	if strings.TrimSpace(choice.Message.Refusal) != "" {
		return "", errors.New("model refused the request")
	}
	switch choice.FinishReason {
	case "length":
		return "", fmt.Errorf("chat completion did not stop: %s: %w", choice.FinishReason, errResponseTruncated)
	case "", "stop":
	default:
		return "", fmt.Errorf("chat completion did not stop: %s", choice.FinishReason)
	}
	if strings.TrimSpace(choice.Message.Content) == "" {
		return "", errors.New("chat completion has empty content")
	}
	return choice.Message.Content, nil
}
