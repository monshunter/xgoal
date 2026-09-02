package validator

import (
	"context"

	"github.com/monshunter/xgoal/internal/protocol"
)

type Request struct {
	ValidatorID      string
	SubjectID        string
	GoalRevisionHash string
	ConfigHash       string
	TreeHash         string
}

type Runner interface {
	Run(context.Context, Request) (protocol.Evidence, error)
}
