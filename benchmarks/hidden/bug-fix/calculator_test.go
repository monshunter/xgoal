package bugfix

import (
	"errors"
	"testing"
)

func TestDivideRejectsZeroAndPreservesNormalDivision(t *testing.T) {
	if _, err := Divide(8, 0); !errors.Is(err, ErrDivisionByZero) {
		t.Fatalf("Divide(8, 0) error = %v", err)
	}
	if value, err := Divide(8, 2); err != nil || value != 4 {
		t.Fatalf("Divide(8, 2) = %d, %v", value, err)
	}
}
