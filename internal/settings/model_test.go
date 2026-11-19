package settings

import "testing"

func TestModelProfilesRequireCleanHTTPS(t *testing.T) {
	ok, err := (ModelProfile{Provider: Responses, BaseURL: " https://API.Example.INVALID/v1/ ", Model: "model"}).Normalize()
	if err != nil || ok.BaseURL != "https://api.example.invalid/v1" {
		t.Fatalf("normalized profile %+v: %v", ok, err)
	}
	for _, base := range []string{
		"http://example.invalid", "https://user@example.invalid", "https://example.invalid/?a=b",
		"https://example.invalid/#fragment", "https://example.invalid/a/../v1", "https://example.invalid/%2fhidden",
	} {
		if _, err := (ModelProfile{Provider: Responses, BaseURL: base, Model: "model"}).Normalize(); err == nil {
			t.Errorf("accepted ambiguous base URL %s", base)
		}
	}
}
