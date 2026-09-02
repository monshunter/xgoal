package lib

import "testing"

func TestMessageNormalizesName(t *testing.T) {
	if got := Message("  Ada  "); got != "hello Ada" {
		t.Fatalf("Message = %q", got)
	}
}
