package llm

import (
	"errors"
	"fmt"
	"strings"

	"github.com/openai/openai-go/v3/responses"
)

// finalJSONText extracts the completed final response, excluding reasoning
// and handling refusals explicitly instead of blindly calling OutputText().
func finalJSONText(response *responses.Response) (string, error) {
	if response == nil {
		return "", errors.New("nil response")
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
