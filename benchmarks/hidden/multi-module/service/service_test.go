package service

import "testing"

func TestGreetingUsesSharedNormalizedName(t *testing.T) {
	if got := Greeting("  Ada  "); got != "hello Ada!" {
		t.Fatalf("Greeting = %q", got)
	}
}
