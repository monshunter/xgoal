package canonical_test

import (
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/canonical"
)

func TestMarshalUsesJCSOrderingAndEscaping(t *testing.T) {
	value := map[string]any{
		"b":      []any{true, nil},
		"a":      "<\n",
		"\ue000": 2,
		"😀":      1,
	}
	got, err := canonical.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\"a\":\"<\\n\",\"b\":[true,null],\"😀\":1,\"\ue000\":2}"
	if string(got) != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}

func TestHashIsDomainSeparatedGolden(t *testing.T) {
	left := map[string]any{"b": "x", "a": 1}
	right := map[string]any{"a": 1, "b": "x"}

	leftHash, err := canonical.Hash("work-packet", "xgoal.work-packet/v1alpha1", left)
	if err != nil {
		t.Fatal(err)
	}
	rightHash, err := canonical.Hash("work-packet", "xgoal.work-packet/v1alpha1", right)
	if err != nil {
		t.Fatal(err)
	}
	const want = "50640be75f78e55dde256ee60fd8043d58f4af3d4e55266656b10d208797e656"
	if leftHash != want || rightHash != want {
		t.Fatalf("Hash() = %q / %q, want %q", leftHash, rightHash, want)
	}

	other, err := canonical.Hash("agent-result", "xgoal.work-packet/v1alpha1", left)
	if err != nil {
		t.Fatal(err)
	}
	if other == leftHash {
		t.Fatal("Hash() did not separate object kinds")
	}
}

func TestMarshalRejectsFloatsAndInvalidUTF8(t *testing.T) {
	if _, err := canonical.Marshal(map[string]any{"cost": 1.25}); err == nil || !strings.Contains(err.Error(), "floating") {
		t.Fatalf("float error = %v", err)
	}
	if _, err := canonical.Marshal(map[string]any{"bad": string([]byte{0xff})}); err == nil || !strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("UTF-8 error = %v", err)
	}
}
