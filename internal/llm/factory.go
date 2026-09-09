package llm

import "fmt"

// New builds a Provider for the given provider name ("ollama" or "openai").
func New(name, baseURL, key string) (Provider, error) {
	switch name {
	case "ollama":
		return NewOllamaProvider(baseURL, key), nil
	case "openai":
		return NewOpenAIProvider(baseURL, key), nil
	default:
		return nil, fmt.Errorf("unsupported provider %q", name)
	}
}
