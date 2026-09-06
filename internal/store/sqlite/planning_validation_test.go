package sqlite

import (
	"errors"
	"github.com/monshunter/xgoal/internal/validationplan"
	"testing"
)

func TestGeneratedApprovalBindsScriptsInputsAndWholePlan(t *testing.T) {
	_, r := planningFixture(t)
	r.GeneratedValidationPolicy = "human-gate"
	p := planningProposal()
	p.Contract.GeneratedValidators = []validationplan.Generated{{ID: "behavior", Description: "required output", Runtime: "sh", Script: "test -s source", TimeoutSeconds: 5}}
	p.Contract.AcceptanceCriteria[0].Validators = []string{"behavior"}
	p.Plan.WorkItems[0].Validators = []string{"behavior"}
	input, err := validationplan.NewInput("acceptance.md", "100644", []byte("original requirement"))
	if err != nil {
		t.Fatal(err)
	}
	p.Contract.AcceptanceInputs = []validationplan.Input{input}
	r.ApprovedValidationHash, err = preparedValidationHash(r, p)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkGeneratedApproval(r, p); err != nil {
		t.Fatal(err)
	}
	p.Contract.GeneratedValidators[0].Script = "true"
	if err := checkGeneratedApproval(r, p); !errors.Is(err, ErrGeneratedValidationApproval) {
		t.Fatalf("changed script approved: %v", err)
	}
	p.Contract.GeneratedValidators[0].Script = "test -s source"
	p.Plan.WorkItems[0].Objective = "different implementation"
	if err := checkGeneratedApproval(r, p); !errors.Is(err, ErrGeneratedValidationApproval) {
		t.Fatalf("changed Work approved: %v", err)
	}
	p.Plan.WorkItems[0].Objective = "implement"
	p.Contract.AcceptanceInputs[0], err = validationplan.NewInput("acceptance.md", "100644", []byte("changed requirement"))
	if err != nil {
		t.Fatal(err)
	}
	if err := checkGeneratedApproval(r, p); !errors.Is(err, ErrGeneratedValidationApproval) {
		t.Fatalf("changed requirement approved: %v", err)
	}
}
