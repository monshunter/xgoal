package codex

import (
	"strconv"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/protocol"
)

func executionArguments(e *config.ExecutionConfig) []string {
	argv := []string{"-c", "developer_instructions=" + strconv.Quote(protocol.DelegationInstructions)}
	if e != nil {
		if e.Model != "" {
			argv = append(argv, "--model", e.Model)
		}
		if e.ReasoningEffort != "" {
			argv = append(argv, "-c", "model_reasoning_effort="+strconv.Quote(e.ReasoningEffort))
		}
	}
	return argv
}
