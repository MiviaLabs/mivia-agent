package provider

import "testing"

func TestNewOpenRouterAppliesDefaultsAndOverrides(t *testing.T) {
	comp, err := NewOpenRouter(Options{APIKey: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	client := comp.(*OpenAICompat)
	if client.httpReferer == "" || client.xTitle != "mivia" || client.baseURL == "" {
		t.Fatalf("client=%+v", client)
	}
	comp, err = NewOpenRouter(Options{APIKey: "fake", BaseURL: "https://example.com/v1", HTTPReferer: "https://ref.example", XTitle: "title"})
	if err != nil {
		t.Fatal(err)
	}
	client = comp.(*OpenAICompat)
	if client.baseURL != "https://example.com/v1" || client.httpReferer != "https://ref.example" || client.xTitle != "title" {
		t.Fatalf("client=%+v", client)
	}
}
