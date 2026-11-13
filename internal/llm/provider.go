package llm

import "github.com/openai/openai-go/v3"

// Provider wraps one OpenAI-compatible client. Models and per-call options are
// passed to each method so entity recognition and translation can use distinct models.
type Provider struct {
	client *openai.Client
}

func NewProvider(client *openai.Client) *Provider {
	return &Provider{client: client}
}
