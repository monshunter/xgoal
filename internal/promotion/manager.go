package promotion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/gitrepo"
)

type Manager struct {
	root       string
	repository *gitrepo.Repository
	journal    Journal
	lock       *sync.Mutex
}

var projectLocks sync.Map

type marker struct {
	ProtocolVersion string            `json:"protocol_version"`
	PromotionID     string            `json:"promotion_id"`
	RequestHash     string            `json:"request_hash"`
	Commit          string            `json:"commit"`
	Tree            string            `json:"tree"`
	Parent          string            `json:"parent"`
	Trailers        map[string]string `json:"trailers"`
	TrailerHash     string            `json:"trailer_hash"`
	MarkerHash      string            `json:"marker_hash"`
}

const markerVersion = "xgoal.promotion-marker/v1"

func NewManager(runtimeRoot string, repository *gitrepo.Repository, journal Journal) (*Manager, error) {
	if repository == nil || journal == nil || !filepath.IsAbs(runtimeRoot) || filepath.Clean(runtimeRoot) != runtimeRoot || strings.ContainsAny(runtimeRoot, "\r\n\x00") {
		return nil, errors.New("promotion manager requires runtime root, repository, and journal")
	}
	if err := ensurePrivateDirectory(runtimeRoot); err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(runtimeRoot)
	if err != nil {
		return nil, err
	}
	root := filepath.Join(resolved, "promotions")
	if err := ensurePrivateDirectory(root); err != nil {
		return nil, err
	}
	lock, _ := projectLocks.LoadOrStore(repository.CommonDir(), &sync.Mutex{})
	return &Manager{root: root, repository: repository, journal: journal, lock: lock.(*sync.Mutex)}, nil
}

func (manager *Manager) Promote(ctx context.Context, request Request) (Observation, error) {
	manager.lock.Lock()
	defer manager.lock.Unlock()
	if err := request.Validate(); err != nil {
		return Observation{}, err
	}
	if request.ExecutionModel != CurrentDirectory {
		return Observation{}, ErrExecutionMigrationRequired
	}
	// Recovery has the same filesystem prerequisite as a first invocation.
	// In particular, an existing marker never authorizes accepting a stale tree.
	if err := manager.verifyCandidate(ctx, request); err != nil {
		return Observation{}, err
	}
	record, _, err := manager.journal.Ensure(ctx, request)
	if err != nil {
		return Observation{}, err
	}
	if !record.Valid() || !EqualRequest(record.Request, request) {
		return Observation{}, errors.New("promotion journal returned a mismatched record")
	}
	if record.State == Failed {
		return Observation{}, errors.New("failed promotion requires an explicit new attempt")
	}
	requestHash, err := request.Hash()
	if err != nil {
		return Observation{}, err
	}
	storedMarker, markerExists, err := manager.readMarker(request, requestHash)
	if err != nil {
		return Observation{}, err
	}
	if !markerExists {
		if err := manager.journal.Preflight(ctx, request); err != nil {
			return Observation{}, manager.fail(ctx, request.ID, err)
		}
		if err := manager.verifyCandidate(ctx, request); err != nil {
			return Observation{}, manager.fail(ctx, request.ID, err)
		}
		created, err := manager.repository.CreateCommit(ctx, gitrepo.CommitSpec{
			Tree: request.CandidateTree, Parent: request.OldCommit,
			Message: commitMessage(request), Timestamp: request.CommitAt,
		})
		if err != nil {
			return Observation{}, manager.fail(ctx, request.ID, err)
		}
		storedMarker, err = manager.writeMarker(request, requestHash, created)
		if err != nil {
			return Observation{}, err
		}
	}
	if _, err := manager.readPromotionCommit(ctx, request, storedMarker); err != nil {
		return Observation{}, err
	}
	if _, err := manager.journal.RecordCommit(ctx, request.ID, storedMarker.Commit); err != nil {
		return Observation{}, err
	}
	current, err := manager.repository.ResolveRef(ctx, request.IntegrationRef)
	if err != nil {
		return Observation{}, err
	}
	switch current.Commit {
	case request.OldCommit:
		if err := manager.journal.Preflight(ctx, request); err != nil {
			return Observation{}, manager.fail(ctx, request.ID, err)
		}
		if err := manager.verifyCandidate(ctx, request); err != nil {
			return Observation{}, manager.fail(ctx, request.ID, err)
		}
		if err := manager.repository.UpdateRefCAS(ctx, request.IntegrationRef, storedMarker.Commit, request.OldCommit); err != nil {
			// CAS can have succeeded before its read-back failed. Preserve the
			// intent so recovery can prove which external effect occurred.
			return Observation{}, err
		}
	case storedMarker.Commit:
		// The external effect completed before its database observation.
	default:
		return Observation{}, fmt.Errorf("%w: integration ref moved to %s", gitrepo.ErrRefConflict, current.Commit)
	}
	if _, err := manager.journal.RecordRefUpdate(ctx, request.ID, storedMarker.Commit); err != nil {
		return Observation{}, err
	}
	observation, err := manager.observe(ctx, request, storedMarker)
	if err != nil {
		// The private ref already changed. A drifted checkout must leave the
		// effect pending and the scene intact, never turn it into an untracked
		// failed effect or advance the accepted checkout state.
		return Observation{}, err
	}
	if _, err := manager.journal.Observe(ctx, request.ID, observation); err != nil {
		return Observation{}, err
	}
	return observation, nil
}

func (manager *Manager) verifyCandidate(ctx context.Context, request Request) error {
	if request.ExecutionModel != CurrentDirectory || request.CheckoutIdentity == nil {
		return ErrExecutionMigrationRequired
	}
	if request.ExecutionPath != manager.repository.Root() || request.CheckoutIdentity.CommonDir != manager.repository.CommonDir() {
		return errors.New("promotion checkout does not belong to this repository")
	}
	base, err := manager.repository.ResolveRevision(ctx, request.OldCommit)
	if err != nil {
		return err
	}
	if base.Tree != request.OldTree {
		return errors.New("promotion base commit and tree do not match")
	}
	return manager.repository.CheckSnapshot(ctx, gitrepo.SnapshotSpec{BaseTree: request.OldTree, ExcludePaths: request.ExcludePaths}, *request.CheckoutIdentity, request.CandidateTree)
}

func (manager *Manager) observe(ctx context.Context, request Request, stored marker) (Observation, error) {
	ref, err := manager.repository.ResolveRef(ctx, request.IntegrationRef)
	if err != nil {
		return Observation{}, err
	}
	commit, err := manager.readPromotionCommit(ctx, request, stored)
	if err != nil {
		return Observation{}, err
	}
	if ref.Commit != stored.Commit || ref.Tree != request.CandidateTree {
		return Observation{}, errors.New("promotion Git read-back does not match commit, tree, parent, and ref")
	}
	if err := manager.verifyCandidate(ctx, request); err != nil {
		return Observation{}, err
	}
	return Observation{
		IntegrationRef: request.IntegrationRef, IntegrationCommit: commit.ID,
		IntegrationTree: commit.Tree, Trailers: promotionTrailers(request),
	}, nil
}

func (manager *Manager) readPromotionCommit(ctx context.Context, request Request, stored marker) (gitrepo.Commit, error) {
	commit, err := manager.repository.ReadCommit(ctx, stored.Commit)
	if err != nil {
		return gitrepo.Commit{}, err
	}
	if commit.Tree != request.CandidateTree || commit.Parent != request.OldCommit || !equalStrings(commit.Trailers, promotionTrailers(request)) || !equalStrings(stored.Trailers, promotionTrailers(request)) {
		return gitrepo.Commit{}, errors.New("promotion commit tree, parent, or trailers do not match request")
	}
	return commit, nil
}

func (manager *Manager) fail(ctx context.Context, id string, cause error) error {
	if err := manager.journal.Fail(ctx, id, cause.Error()); err != nil {
		return errors.Join(cause, err)
	}
	return cause
}

func (manager *Manager) markerPath(request Request) string {
	return filepath.Join(manager.root, request.ID, "marker.json")
}

func (manager *Manager) readMarker(request Request, requestHash string) (marker, bool, error) {
	filename := manager.markerPath(request)
	info, err := os.Lstat(filename)
	if errors.Is(err, os.ErrNotExist) {
		return marker{}, false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o600 || info.Size() > 1<<20 {
		return marker{}, false, errors.New("promotion marker is missing or unsafe")
	}
	content, err := os.ReadFile(filename)
	if err != nil {
		return marker{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var stored marker
	if err := decoder.Decode(&stored); err != nil {
		return marker{}, false, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return marker{}, false, errors.New("promotion marker contains trailing data")
	}
	if err := validateMarker(stored, request, requestHash); err != nil {
		return marker{}, false, err
	}
	return stored, true, nil
}

func (manager *Manager) writeMarker(request Request, requestHash string, commit gitrepo.Commit) (marker, error) {
	directory := filepath.Dir(manager.markerPath(request))
	if err := ensurePrivateDirectory(directory); err != nil {
		return marker{}, err
	}
	trailers := promotionTrailers(request)
	trailerHash, err := canonical.Hash("promotion-trailers", markerVersion, trailers)
	if err != nil {
		return marker{}, err
	}
	stored := marker{
		ProtocolVersion: markerVersion, PromotionID: request.ID, RequestHash: requestHash,
		Commit: commit.ID, Tree: commit.Tree, Parent: commit.Parent, Trailers: trailers, TrailerHash: trailerHash,
	}
	stored.MarkerHash, err = markerHash(stored)
	if err != nil {
		return marker{}, err
	}
	content, err := canonical.Marshal(stored)
	if err != nil {
		return marker{}, err
	}
	temporary, err := os.CreateTemp(directory, ".marker-")
	if err != nil {
		return marker{}, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return marker{}, err
	}
	if _, err := temporary.Write(content); err != nil {
		_ = temporary.Close()
		return marker{}, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return marker{}, err
	}
	if err := temporary.Close(); err != nil {
		return marker{}, err
	}
	if err := os.Link(temporaryPath, manager.markerPath(request)); err != nil {
		return marker{}, fmt.Errorf("publish promotion marker without overwrite: %w", err)
	}
	if err := syncDirectory(directory); err != nil {
		return marker{}, err
	}
	return stored, nil
}

func validateMarker(stored marker, request Request, requestHash string) error {
	if stored.ProtocolVersion != markerVersion || stored.PromotionID != request.ID || stored.RequestHash != requestHash || !validObjectID(stored.Commit) ||
		stored.Tree != request.CandidateTree || stored.Parent != request.OldCommit || !equalStrings(stored.Trailers, promotionTrailers(request)) {
		return errors.New("promotion marker does not match request")
	}
	trailerHash, err := canonical.Hash("promotion-trailers", markerVersion, stored.Trailers)
	if err != nil || trailerHash != stored.TrailerHash {
		return errors.New("promotion marker trailer hash mismatch")
	}
	hash, err := markerHash(stored)
	if err != nil || hash != stored.MarkerHash {
		return errors.New("promotion marker hash mismatch")
	}
	return nil
}

func markerHash(stored marker) (string, error) {
	identity := stored
	identity.MarkerHash = ""
	return canonical.Hash("promotion-marker", markerVersion, identity)
}

func commitMessage(request Request) string {
	trailers := promotionTrailers(request)
	return fmt.Sprintf("xgoal: promote %s\n\nXGoal-Goal: %s\nXGoal-Goal-Revision: %s\nXGoal-Work-Item: %s\nXGoal-Attempt: %s\nXGoal-Evidence-Set: %s\n",
		request.WorkItemID, trailers["XGoal-Goal"], trailers["XGoal-Goal-Revision"],
		trailers["XGoal-Work-Item"], trailers["XGoal-Attempt"], trailers["XGoal-Evidence-Set"])
}

func promotionTrailers(request Request) map[string]string {
	return map[string]string{
		"XGoal-Goal": request.GoalID, "XGoal-Goal-Revision": strconv.FormatInt(request.GoalRevision, 10),
		"XGoal-Work-Item": request.WorkItemID, "XGoal-Attempt": request.AttemptID,
		"XGoal-Evidence-Set": request.EvidenceSetID,
	}
}

func equalStrings(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func ensurePrivateDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return err
		}
		info, err = os.Lstat(directory)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("promotion directory is missing or unsafe")
	}
	return os.Chmod(directory, 0o700)
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}
