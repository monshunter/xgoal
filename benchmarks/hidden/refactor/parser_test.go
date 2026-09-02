package refactor

import "testing"

func TestNormalizeCompatibility(t *testing.T) {
	cases := map[string]string{"  HELLO   World ": "hello world", "Already-normal": "already-normal", "": ""}
	for input, want := range cases {
		if got := Normalize(input); got != want {
			t.Fatalf("Normalize(%q) = %q, want %q", input, got, want)
		}
	}
}
