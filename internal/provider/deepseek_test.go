package provider

import "testing"

func TestNewDeepSeekUsesDefaultAndExplicitBaseURL(t *testing.T) {
	for name, opts := range map[string]Options{
		"default":  {APIKey: "fake"},
		"explicit": {APIKey: "fake", BaseURL: "https://example.com/v1"},
	} {
		t.Run(name, func(t *testing.T) {
			comp, err := NewDeepSeek(opts)
			if err != nil {
				t.Fatal(err)
			}
			client := comp.(*OpenAICompat)
			if client.name != "deepseek" || client.baseURL == "" {
				t.Fatalf("client=%+v", client)
			}
			if name == "default" && client.baseURL != "https://api.deepseek.com/v1" {
				t.Fatalf("expected default baseURL, got %q", client.baseURL)
			}
			if name == "explicit" && client.baseURL != opts.BaseURL {
				t.Fatalf("baseURL=%q", client.baseURL)
			}
		})
	}
}
