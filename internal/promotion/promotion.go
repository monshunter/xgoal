package promotion

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/gitrepo"
)

type State string

const (
	Requested     State = "REQUESTED"
	CommitCreated State = "COMMIT_CREATED"
	RefUpdated    State = "REF_UPDATED"
	Observed      State = "OBSERVED"
	Failed        State = "FAILED"
)

const CurrentDirectory = "current-directory"

var ErrExecutionMigrationRequired = errors.New("EXECUTION_MIGRATION_REQUIRED: legacy worktree promotion is read-only; preserve its history and resolve migration before execution")

type Request struct {
	ID                 string    `json:"id"`
	EffectID           string    `json:"effect_id"`
	EffectKey          string    `json:"effect_key"`
	GoalID             string    `json:"goal_id"`
	GoalRevision       int64     `json:"goal_revision"`
	GoalRevisionHash   string    `json:"goal_revision_hash"`
	ConfigHash         string    `json:"config_hash"`
	WorkItemID         string    `json:"work_item_id"`
	AttemptID          string    `json:"attempt_id"`
	LeaseID            string    `json:"lease_id"`
	LeaseGeneration    int64     `json:"lease_generation"`
	BundleHash         string    `json:"bundle_hash"`
	EvidenceSetID      string    `json:"evidence_set_id"`
	IntegrationRef     string    `json:"integration_ref"`
	OldCommit          string    `json:"old_commit"`
	OldTree            string    `json:"old_tree"`
	CandidateTree      string    `json:"candidate_tree"`
	ValidationWorktree string    `json:"validation_worktree"`
	CommitAt           time.Time `json:"commit_at"`
	// Added fields are absent from legacy canonical requests. Never populate
	// them while reading historical effects or recomputing their hashes.
	ExecutionModel   string                    `json:"execution_model,omitempty"`
	ExecutionPath    string                    `json:"execution_path,omitempty"`
	CheckoutIdentity *gitrepo.CheckoutIdentity `json:"checkout_identity,omitempty"`
	ExcludePaths     []string                  `json:"exclude_paths,omitempty"`
}

type Record struct {
	Request
	State             State
	IntegrationCommit string
	Version           int64
}

type Observation struct {
	IntegrationRef    string            `json:"integration_ref"`
	IntegrationCommit string            `json:"integration_commit"`
	IntegrationTree   string            `json:"integration_tree"`
	Trailers          map[string]string `json:"trailers"`
}

type Journal interface {
	Ensure(context.Context, Request) (Record, bool, error)
	Preflight(context.Context, Request) error
	RecordCommit(context.Context, string, string) (Record, error)
	RecordRefUpdate(context.Context, string, string) (Record, error)
	Observe(context.Context, string, Observation) (Record, error)
	Fail(context.Context, string, string) error
}

func (request Request) Validate() error {
	for _, value := range []string{request.ID, request.EffectID, request.EffectKey, request.GoalID, request.WorkItemID, request.AttemptID, request.LeaseID, request.EvidenceSetID} {
		if !validLabel(value) {
			return errors.New("promotion identity fields must be non-empty single-line values")
		}
	}
	if request.GoalRevision <= 0 || request.LeaseGeneration <= 0 || !validHash(request.GoalRevisionHash, 64) || !validHash(request.ConfigHash, 64) || !validHash(request.BundleHash, 64) ||
		!validObjectID(request.OldCommit) || !validObjectID(request.OldTree) || !validObjectID(request.CandidateTree) ||
		!validLabel(request.IntegrationRef) || request.CommitAt.IsZero() {
		return errors.New("promotion request contains an invalid revision, hash, ref, or timestamp")
	}
	switch request.ExecutionModel {
	case "":
		if !strings.HasPrefix(request.IntegrationRef, "refs/heads/") || !cleanAbsolute(request.ValidationWorktree) ||
			request.ExecutionPath != "" || request.CheckoutIdentity != nil || len(request.ExcludePaths) != 0 {
			return errors.New("legacy promotion contains invalid or mixed execution fields")
		}
	case CurrentDirectory:
		identity := request.CheckoutIdentity
		if filepath.Base(request.ID) != request.ID || request.ID == "." || request.ID == ".." || strings.ContainsRune(request.ID, '\\') ||
			request.ValidationWorktree != "" || !cleanAbsolute(request.ExecutionPath) || identity == nil ||
			identity.Root != request.ExecutionPath || identity.Validate() != nil ||
			!validPrivateRef(request.IntegrationRef) || request.IntegrationRef != "refs/xgoal/goals/"+request.GoalID+"/integration" {
			return errors.New("current-directory promotion must bind its checkout identity and private Goal ref")
		}
		for _, path := range request.ExcludePaths {
			relative, err := filepath.Rel(path, request.ExecutionPath)
			if !cleanAbsolute(path) || err != nil || relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative) {
				return errors.New("promotion snapshot exclusions must be absolute and cannot contain the execution root")
			}
		}
	default:
		return errors.New("unknown promotion execution model")
	}
	return nil
}

func (request Request) Hash() (string, error) {
	if err := request.Validate(); err != nil {
		return "", err
	}
	version := "xgoal.promotion/v1alpha1"
	if request.ExecutionModel == CurrentDirectory {
		version = "xgoal.promotion/v2"
	}
	return canonical.Hash("promotion-request", version, request)
}

func EqualRequest(left, right Request) bool {
	leftHash, leftErr := left.Hash()
	rightHash, rightErr := right.Hash()
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

func (record Record) Valid() bool {
	if record.Request.Validate() != nil || record.Version <= 0 {
		return false
	}
	switch record.State {
	case Requested:
		return record.IntegrationCommit == ""
	case CommitCreated, RefUpdated, Observed:
		return validObjectID(record.IntegrationCommit)
	case Failed:
		return record.IntegrationCommit == "" || validObjectID(record.IntegrationCommit)
	default:
		return false
	}
}

func (observation Observation) Validate() error {
	if (!strings.HasPrefix(observation.IntegrationRef, "refs/heads/") && !validPrivateRef(observation.IntegrationRef)) || !validObjectID(observation.IntegrationCommit) || !validObjectID(observation.IntegrationTree) {
		return errors.New("invalid promotion observation")
	}
	for key, value := range observation.Trailers {
		if !strings.HasPrefix(key, "XGoal-") || !validLabel(value) {
			return fmt.Errorf("invalid promotion trailer %q", key)
		}
	}
	return nil
}

func cleanAbsolute(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func validPrivateRef(ref string) bool {
	const prefix, suffix = "refs/xgoal/goals/", "/integration"
	if !strings.HasPrefix(ref, prefix) || !strings.HasSuffix(ref, suffix) {
		return false
	}
	goal := strings.TrimSuffix(strings.TrimPrefix(ref, prefix), suffix)
	if !validLabel(goal) || strings.ContainsAny(goal, "/\\ ~^:?*[") || strings.HasPrefix(goal, ".") || strings.HasSuffix(goal, ".") || strings.HasSuffix(goal, ".lock") || strings.Contains(goal, "..") || strings.Contains(goal, "@{") {
		return false
	}
	for _, character := range goal {
		if character < 0x20 || character == 0x7f {
			return false
		}
	}
	return true
}

func validLabel(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}

func validObjectID(value string) bool {
	return validHash(value, 40) || validHash(value, 64)
}

func validHash(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
