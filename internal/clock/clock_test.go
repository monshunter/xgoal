package clock_test

import (
	"testing"
	"time"

	"github.com/monshunter/xgoal/internal/clock"
)

func TestFakeAdvancesDeterministically(t *testing.T) {
	start := time.Date(2026, 9, 2, 10, 0, 0, 0, time.UTC)
	fake := clock.NewFake(start)
	if got := fake.Now(); !got.Equal(start) {
		t.Fatalf("Now() = %s, want %s", got, start)
	}
	fake.Advance(90 * time.Second)
	if got := fake.Now(); !got.Equal(start.Add(90 * time.Second)) {
		t.Fatalf("Now() = %s", got)
	}
}
