package claude

import (
	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/protocol"
)

func executionArguments(e *config.ExecutionConfig) []string {
	argv := []string{"--append-system-prompt", protocol.DelegationInstructions}
	if e != nil {
		if e.Model != "" {
			argv = append(argv, "--model", e.Model)
		}
		if e.ReasoningEffort != "" {
			argv = append(argv, "--effort", e.ReasoningEffort)
		}
	}
	return argv
}
