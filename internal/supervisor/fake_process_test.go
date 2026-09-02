package supervisor_test

import (
	"context"
	"errors"
	"testing"

	"github.com/monshunter/xgoal/internal/supervisor"
)

func TestFakeProcessCompletesOrTerminates(t *testing.T) {
	completed := supervisor.NewFakeProcess(101)
	completed.Complete(supervisor.ProcessResult{ExitCode: 0})
	result, err := completed.Wait(context.Background())
	if err != nil || result.ExitCode != 0 {
		t.Fatalf("Wait() = %+v, %v", result, err)
	}

	terminated := supervisor.NewFakeProcess(102)
	if err := terminated.Terminate(); err != nil {
		t.Fatal(err)
	}
	if _, err := terminated.Wait(context.Background()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Wait() error = %v, want canceled", err)
	}
}
