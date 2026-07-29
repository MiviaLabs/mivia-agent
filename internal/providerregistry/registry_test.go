package providerregistry

import "testing"

func TestLookupAndNamesAreStable(t *testing.T) {
	d, ok := Lookup("DeepSeek")
	if !ok || d.DefaultModel != "deepseek-v4-flash" || d.DefaultAPIKeyEnv != "DEEPSEEK_API_KEY" {
		t.Fatalf("descriptor=%+v ok=%v", d, ok)
	}
	names := Names()
	if len(names) != 3 || names[0] != "deepseek" || names[1] != "openrouter" || names[2] != "zai" {
		t.Fatalf("names=%v", names)
	}
	names[0] = "mutated"
	if next := Names(); next[0] != "deepseek" {
		t.Fatalf("Names returned aliased storage: %v", next)
	}
	zai, ok := Lookup("ZAI")
	if !ok || zai.DefaultModel != "glm-5.2" || zai.DefaultURL != "https://api.z.ai/api/paas/v4" || zai.DefaultAPIKeyEnv != "ZAI_API_KEY" {
		t.Fatalf("zai descriptor=%+v ok=%v", zai, ok)
	}
}
