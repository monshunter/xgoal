package validationplan

import (
	"strings"
	"testing"
)

func TestGeneratedValidationRejectsUnboundedOrInvalidCommands(t *testing.T) {
	valid := Generated{ID: "behavior", Description: "required output", Runtime: "sh", Script: "test -s output.txt", TimeoutSeconds: 5}
	for _, tc := range []struct {
		name   string
		change func(*Generated)
	}{
		{"unknown runtime", func(g *Generated) { g.Runtime = "bash" }},
		{"empty script", func(g *Generated) { g.Script = " " }},
		{"oversized script", func(g *Generated) { g.Script = strings.Repeat("a", (64<<10)+1) }},
		{"zero timeout", func(g *Generated) { g.TimeoutSeconds = 0 }},
		{"unbounded timeout", func(g *Generated) { g.TimeoutSeconds = 121 }},
		{"path id", func(g *Generated) { g.ID = "../behavior" }},
		{"NUL", func(g *Generated) { g.Script = "a\x00b" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := valid
			tc.change(&g)
			if ValidateGenerated([]Generated{g}) == nil {
				t.Fatal("accepted invalid command")
			}
		})
	}
	if ValidateGenerated([]Generated{valid, valid}) == nil {
		t.Fatal("accepted duplicate ID")
	}
}
