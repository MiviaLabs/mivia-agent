package providerregistry

import "testing"

func TestLookupAndNamesAreStable(t *testing.T) {
	d, ok := Lookup("DeepSeek")
	if !ok || d.DefaultModel != "deepseek-v4-flash" || d.DefaultAPIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("descriptor=%+v ok=%v", d, ok)
	}
	names := Names()
	if len(names) != 2 || names[0] != "deepseek" || names[1] != "openrouter" {
		t.Fatalf("names=%v", names)
	}
	names[0] = "mutated"
	if next := Names(); next[0] != "deepseek" {
		t.Fatalf("Names returned aliased storage: %v", next)
	}
}
