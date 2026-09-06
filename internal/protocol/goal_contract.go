package protocol

import (
	"encoding/json"
	"fmt"
	"github.com/monshunter/xgoal/internal/canonical"
)

func validateEmbeddedGoalContract(raw json.RawMessage, expected string) error {
	if len(raw) == 0 {
		return nil
	}
	var object map[string]json.RawMessage
	if len(raw) > 8<<20 || json.Unmarshal(raw, &object) != nil || object == nil {
		return fmt.Errorf("invalid embedded Goal Contract")
	}
	hash, err := canonical.Hash("goal-revision", "xgoal.goal-revision/v1", raw)
	if err != nil || hash != expected {
		return fmt.Errorf("embedded Goal Contract differs from the frozen revision hash")
	}
	return nil
}
