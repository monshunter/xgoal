package redact_test

import (
	"testing"

	"github.com/monshunter/xgoal/internal/redact"
)

func TestStringAndValueRemoveCommonCredentialForms(t *testing.T) {
	t.Parallel()

	raw := "Authorization: Bearer secret-value API_TOKEN=abc123 sk-ant-abcdefghijklmnop"
	redacted := redact.String(raw)
	for _, secret := range []string{"secret-value", "abc123", "sk-ant-abcdefghijklmnop"} {
		if contains(redacted, secret) {
			t.Fatalf("redacted string retained %q: %s", secret, redacted)
		}
	}
	value := redact.Value(map[string]any{"nested": []any{raw}, "api_token": "plain-secret"}).(map[string]any)
	if contains(value["nested"].([]any)[0].(string), "abc123") {
		t.Fatal("recursive redaction retained secret")
	}
	if value["api_token"] != "[REDACTED]" {
		t.Fatalf("sensitive JSON key was not redacted: %#v", value)
	}
	quoted := redact.String(`provider error: {"api_token":"plain-secret","authorization":"Bearer bearer-secret"}`)
	if contains(quoted, "plain-secret") || contains(quoted, "bearer-secret") {
		t.Fatalf("quoted nested JSON was not redacted: %s", quoted)
	}
}

func contains(value, part string) bool {
	for index := 0; index+len(part) <= len(value); index++ {
		if value[index:index+len(part)] == part {
			return true
		}
	}
	return false
}
