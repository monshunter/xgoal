// Package benchmark implements the local, non-uploading v0.1 comparison harness.
package benchmark

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
)

const (
	SuiteVersion  = "xgoal.benchmark-suite/v1"
	ResultVersion = "xgoal.benchmark-result/v1"
)

type GroupKind string

const (
	GroupNative        GroupKind = "native"
	GroupAutoGoSingle  GroupKind = "autogo-single"
	GroupXGoalStandard GroupKind = "xgoal-standard"
)

type Category string

const (
	CategoryBug         Category = "bug-fix"
	CategoryFeature     Category = "feature"
	CategoryRefactor    Category = "refactor"
	CategoryEnvironment Category = "environment"
	CategoryRecovery    Category = "recovery"
	CategoryMultiModule Category = "multi-module"
)

type Suite struct {
	ProtocolVersion string  `json:"protocol_version"`
	Name            string  `json:"name"`
	Repetitions     int64   `json:"repetitions"`
	Groups          []Group `json:"groups"`
	Tasks           []Task  `json:"tasks"`
	root            string
}

type Group struct {
	ID   string    `json:"id"`
	Kind GroupKind `json:"kind"`
}

type Task struct {
	ID             string   `json:"id"`
	Category       Category `json:"category"`
	Fixture        string   `json:"fixture"`
	FixtureHash    string   `json:"fixture_hash"`
	InitialTree    string   `json:"initial_tree,omitempty"`
	InitialCommit  string   `json:"initial_commit,omitempty"`
	HiddenOverlay  string   `json:"hidden_overlay,omitempty"`
	Goal           string   `json:"goal"`
	AcceptanceArgv []string `json:"acceptance_argv"`
	TimeoutSeconds int64    `json:"timeout_seconds"`
}

type ComparisonIdentity struct {
	TaskID         string `json:"task_id"`
	GroupID        string `json:"group_id"`
	FixtureHash    string `json:"fixture_hash"`
	GoalHash       string `json:"goal_hash"`
	AcceptanceHash string `json:"acceptance_hash"`
	TimeoutHash    string `json:"timeout_hash"`
	TaskHash       string `json:"task_hash"`
}

func Load(path string) (Suite, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Suite{}, err
	}
	file, err := os.Open(absolute)
	if err != nil {
		return Suite{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, 1<<20))
	decoder.DisallowUnknownFields()
	var suite Suite
	if err := decoder.Decode(&suite); err != nil {
		return Suite{}, fmt.Errorf("decode benchmark suite: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return Suite{}, errors.New("benchmark suite must contain one JSON value")
	}
	suite.root = filepath.Dir(absolute)
	if err := suite.Validate(); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func (suite Suite) Validate() error {
	if suite.ProtocolVersion != SuiteVersion || strings.TrimSpace(suite.Name) == "" || suite.Repetitions <= 0 || suite.Repetitions > 100 || !filepath.IsAbs(suite.root) {
		return errors.New("invalid benchmark suite header")
	}
	wantGroups := map[GroupKind]bool{GroupNative: false, GroupAutoGoSingle: false, GroupXGoalStandard: false}
	groupIDs := make(map[string]struct{}, len(suite.Groups))
	for _, group := range suite.Groups {
		if !safeID(group.ID) {
			return errors.New("invalid benchmark group id")
		}
		if _, exists := groupIDs[group.ID]; exists {
			return fmt.Errorf("duplicate benchmark group %q", group.ID)
		}
		if _, exists := wantGroups[group.Kind]; !exists || wantGroups[group.Kind] {
			return fmt.Errorf("invalid or duplicate benchmark group kind %q", group.Kind)
		}
		groupIDs[group.ID], wantGroups[group.Kind] = struct{}{}, true
	}
	if len(groupIDs) != 3 {
		return errors.New("benchmark suite requires native, autogo-single, and xgoal-standard groups")
	}
	ids := make(map[string]struct{}, len(suite.Tasks))
	for _, task := range suite.Tasks {
		if err := suite.validateTask(task); err != nil {
			return err
		}
		if _, exists := ids[task.ID]; exists {
			return fmt.Errorf("duplicate benchmark task %q", task.ID)
		}
		ids[task.ID] = struct{}{}
	}
	if len(ids) == 0 {
		return errors.New("benchmark suite has no tasks")
	}
	return nil
}

func (suite Suite) ValidateReleaseCoverage() error {
	if err := suite.Validate(); err != nil {
		return err
	}
	want := map[Category]bool{CategoryBug: false, CategoryFeature: false, CategoryRefactor: false, CategoryEnvironment: false, CategoryRecovery: false, CategoryMultiModule: false}
	for _, task := range suite.Tasks {
		want[task.Category] = true
		if task.InitialTree == "" || task.InitialCommit == "" || task.HiddenOverlay == "" {
			return fmt.Errorf("release task %q lacks fixed Git or hidden acceptance identity", task.ID)
		}
	}
	for category, found := range want {
		if !found {
			return fmt.Errorf("benchmark release suite lacks category %s", category)
		}
	}
	return nil
}

func (suite Suite) validateTask(task Task) error {
	if !safeID(task.ID) || !validCategory(task.Category) || !safeRelative(task.Fixture) || !validHex(task.FixtureHash, 64) || strings.TrimSpace(task.Goal) == "" || len(task.AcceptanceArgv) == 0 || task.TimeoutSeconds <= 0 || task.TimeoutSeconds > 86400 {
		return fmt.Errorf("invalid benchmark task %q", task.ID)
	}
	if task.HiddenOverlay != "" && !safeRelative(task.HiddenOverlay) {
		return fmt.Errorf("invalid hidden acceptance path for %q", task.ID)
	}
	if (task.InitialTree == "") != (task.InitialCommit == "") || (task.InitialTree != "" && (!validHex(task.InitialTree, 40) || !validHex(task.InitialCommit, 40))) {
		return fmt.Errorf("invalid fixed Git identity for %q", task.ID)
	}
	fixturePath, err := suite.resolve(task.Fixture)
	if err != nil {
		return err
	}
	actual, err := HashFixture(fixturePath)
	if err != nil {
		return fmt.Errorf("hash fixture %q: %w", task.ID, err)
	}
	if actual != task.FixtureHash {
		return fmt.Errorf("fixture %q hash mismatch: want %s got %s", task.ID, task.FixtureHash, actual)
	}
	if task.HiddenOverlay != "" {
		hidden, err := suite.resolve(task.HiddenOverlay)
		if err != nil {
			return err
		}
		if info, err := os.Lstat(hidden); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("hidden acceptance overlay for %q is unavailable", task.ID)
		}
	}
	return nil
}

func (suite Suite) CanonicalJSON() ([]byte, error) {
	copy := suite
	copy.root = ""
	return canonical.Marshal(copy)
}

func (suite Suite) Hash() (string, error) {
	copy := suite
	copy.root = ""
	return canonical.Hash("benchmark-suite", SuiteVersion, copy)
}

func (suite Suite) ComparisonIdentities() ([]ComparisonIdentity, error) {
	if err := suite.Validate(); err != nil {
		return nil, err
	}
	groups := append([]Group(nil), suite.Groups...)
	tasks := append([]Task(nil), suite.Tasks...)
	sort.Slice(groups, func(i, j int) bool { return groups[i].ID < groups[j].ID })
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].ID < tasks[j].ID })
	var result []ComparisonIdentity
	for _, task := range tasks {
		goalHash, _ := canonical.Hash("benchmark-goal", SuiteVersion, task.Goal)
		acceptanceHash, _ := canonical.Hash("benchmark-acceptance", SuiteVersion, task.AcceptanceArgv)
		timeoutHash, _ := canonical.Hash("benchmark-timeout", SuiteVersion, task.TimeoutSeconds)
		taskHash, _ := canonical.Hash("benchmark-task", SuiteVersion, struct {
			Fixture    string `json:"fixture_hash"`
			Goal       string `json:"goal_hash"`
			Acceptance string `json:"acceptance_hash"`
			Timeout    string `json:"timeout_hash"`
		}{task.FixtureHash, goalHash, acceptanceHash, timeoutHash})
		for _, group := range groups {
			result = append(result, ComparisonIdentity{TaskID: task.ID, GroupID: group.ID, FixtureHash: task.FixtureHash, GoalHash: goalHash, AcceptanceHash: acceptanceHash, TimeoutHash: timeoutHash, TaskHash: taskHash})
		}
	}
	return result, nil
}

type RunRequest struct {
	Group     Group
	Task      Task
	Run       int64
	Workspace string
	Goal      string
}

type RunnerResult struct {
	ClaimedCompleted   bool
	ExitCode           int
	Attempts           int64
	HumanInterventions int64
	RegressionFailures int64
	RecoverySuccess    bool
	NoProgressAttempts int64
	Failure            string
}

type Runner interface {
	Run(context.Context, RunRequest) (RunnerResult, error)
}

type RunnerFunc func(context.Context, RunRequest) (RunnerResult, error)

func (function RunnerFunc) Run(ctx context.Context, request RunRequest) (RunnerResult, error) {
	return function(ctx, request)
}

type RunResult struct {
	TaskID                 string `json:"task_id"`
	GroupID                string `json:"group_id"`
	Run                    int64  `json:"run"`
	FixtureHash            string `json:"fixture_hash,omitempty"`
	InitialTree            string `json:"initial_tree,omitempty"`
	InitialCommit          string `json:"initial_commit,omitempty"`
	RunnerClaimedCompleted bool   `json:"runner_claimed_completed"`
	FinalAcceptancePass    bool   `json:"final_acceptance_pass"`
	FalseCompleted         bool   `json:"false_completed"`
	FirstAttemptPass       bool   `json:"first_attempt_pass"`
	RegressionFailures     int64  `json:"regression_failures"`
	HumanInterventions     int64  `json:"human_interventions"`
	RecoverySuccess        bool   `json:"recovery_success"`
	NoProgressAttempts     int64  `json:"no_progress_attempts"`
	Attempts               int64  `json:"attempts"`
	ExitCode               int    `json:"exit_code"`
	AcceptanceExitCode     int    `json:"acceptance_exit_code"`
	Failure                string `json:"failure,omitempty"`
	StartedAt              string `json:"started_at"`
	CompletedAt            string `json:"completed_at"`
	WallTime               Metric `json:"wall_time"`
}

type Metric struct {
	Known bool   `json:"known"`
	Value int64  `json:"value"`
	Unit  string `json:"unit"`
}

func Run(ctx context.Context, suite Suite, groupID string, run int64, runner Runner, now func() time.Time) (RunResult, error) {
	if err := suite.Validate(); err != nil {
		return RunResult{}, err
	}
	if runner == nil || now == nil || run <= 0 || run > suite.Repetitions {
		return RunResult{}, errors.New("invalid benchmark run request")
	}
	group, found := findGroup(suite.Groups, groupID)
	if !found {
		return RunResult{}, fmt.Errorf("unknown benchmark group %q", groupID)
	}
	if len(suite.Tasks) != 1 {
		return RunResult{}, errors.New("Run requires a one-task suite projection")
	}
	task := suite.Tasks[0]
	fixturePath, _ := suite.resolve(task.Fixture)
	workspace, cleanup, tree, commit, err := prepareWorkspace(fixturePath)
	if err != nil {
		return RunResult{}, err
	}
	defer cleanup()
	if task.InitialTree != "" && (tree != task.InitialTree || commit != task.InitialCommit) {
		return RunResult{}, errors.New("prepared Git identity does not match fixed benchmark fixture")
	}
	started := now().UTC()
	runContext, cancel := context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	returned, runErr := runner.Run(runContext, RunRequest{Group: group, Task: task, Run: run, Workspace: workspace, Goal: task.Goal})
	cancel()
	if runErr != nil && returned.Failure == "" {
		returned.Failure = runErr.Error()
	}
	if task.HiddenOverlay != "" {
		hidden, _ := suite.resolve(task.HiddenOverlay)
		if err := copyDirectory(hidden, workspace, false); err != nil {
			return RunResult{}, err
		}
	}
	acceptanceContext, acceptanceCancel := context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*time.Second)
	acceptance := exec.CommandContext(acceptanceContext, task.AcceptanceArgv[0], task.AcceptanceArgv[1:]...)
	acceptance.Dir = workspace
	acceptance.Stdout, acceptance.Stderr = io.Discard, io.Discard
	acceptanceErr := acceptance.Run()
	acceptanceCancel()
	acceptanceExit := exitCode(acceptanceErr)
	completed := now().UTC()
	result := RunResult{
		TaskID: task.ID, GroupID: group.ID, Run: run, FixtureHash: task.FixtureHash, InitialTree: tree, InitialCommit: commit,
		RunnerClaimedCompleted: returned.ClaimedCompleted, FinalAcceptancePass: acceptanceErr == nil,
		FirstAttemptPass: acceptanceErr == nil && returned.Attempts == 1, RegressionFailures: returned.RegressionFailures,
		HumanInterventions: returned.HumanInterventions, RecoverySuccess: returned.RecoverySuccess, NoProgressAttempts: returned.NoProgressAttempts,
		Attempts: returned.Attempts, ExitCode: returned.ExitCode, AcceptanceExitCode: acceptanceExit, Failure: returned.Failure,
		StartedAt: started.Format(time.RFC3339Nano), CompletedAt: completed.Format(time.RFC3339Nano),
		WallTime: Metric{Known: true, Value: completed.Sub(started).Milliseconds(), Unit: "millisecond"},
	}
	result.FalseCompleted = result.RunnerClaimedCompleted && !result.FinalAcceptancePass
	return result, nil
}

func ProjectTask(suite Suite, taskID string) (Suite, error) {
	for _, task := range suite.Tasks {
		if task.ID == taskID {
			copy := suite
			copy.Tasks = []Task{task}
			return copy, nil
		}
	}
	return Suite{}, fmt.Errorf("unknown benchmark task %q", taskID)
}

func HashFixture(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("fixture root must be a real directory")
	}
	type entry struct {
		Path       string `json:"path"`
		Executable bool   `json:"executable"`
		Hash       string `json:"hash"`
	}
	var entries []entry
	err = filepath.WalkDir(absolute, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absolute {
			return nil
		}
		relative, _ := filepath.Rel(absolute, path)
		if relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator)) {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("benchmark fixtures cannot contain symlinks")
		}
		if item.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return errors.New("benchmark fixtures may contain only regular files")
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, entry{Path: filepath.ToSlash(relative), Executable: info.Mode().Perm()&0o111 != 0, Hash: bytesHash(contents)})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return canonical.Hash("benchmark-fixture", SuiteVersion, entries)
}

func prepareWorkspace(fixture string) (string, func(), string, string, error) {
	workspace, err := os.MkdirTemp("", "xgoal-benchmark-")
	if err != nil {
		return "", nil, "", "", err
	}
	cleanup := func() { _ = os.RemoveAll(workspace) }
	if err := copyDirectory(fixture, workspace, true); err != nil {
		cleanup()
		return "", nil, "", "", err
	}
	commands := [][]string{{"init", "-q"}, {"add", "--all"}}
	for _, argv := range commands {
		command := exec.Command("git", argv...)
		command.Dir = workspace
		if output, err := command.CombinedOutput(); err != nil {
			cleanup()
			return "", nil, "", "", fmt.Errorf("prepare benchmark Git repository: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	tree, err := gitOutput(workspace, []string{"write-tree"}, nil)
	if err != nil {
		cleanup()
		return "", nil, "", "", err
	}
	environment := append(os.Environ(), "GIT_AUTHOR_NAME=xgoal benchmark", "GIT_AUTHOR_EMAIL=benchmark@invalid", "GIT_AUTHOR_DATE=2000-01-01T00:00:00Z", "GIT_COMMITTER_NAME=xgoal benchmark", "GIT_COMMITTER_EMAIL=benchmark@invalid", "GIT_COMMITTER_DATE=2000-01-01T00:00:00Z")
	commit, err := gitOutput(workspace, []string{"commit-tree", tree, "-m", "xgoal benchmark fixture"}, environment)
	if err != nil {
		cleanup()
		return "", nil, "", "", err
	}
	if _, err := gitOutput(workspace, []string{"update-ref", "refs/heads/main", commit}, nil); err != nil {
		cleanup()
		return "", nil, "", "", err
	}
	return workspace, cleanup, tree, commit, nil
}

func copyDirectory(source, target string, excludeGit bool) error {
	return filepath.WalkDir(source, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, _ := filepath.Rel(source, path)
		if relative == "." {
			return nil
		}
		if excludeGit && (relative == ".git" || strings.HasPrefix(relative, ".git"+string(filepath.Separator))) {
			if item.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("benchmark copy rejects symlinks")
		}
		destination := filepath.Join(target, relative)
		if item.IsDir() {
			return os.MkdirAll(destination, 0o700)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o600)
		if info.Mode().Perm()&0o111 != 0 {
			mode = 0o700
		}
		return os.WriteFile(destination, contents, mode)
	})
}

func gitOutput(directory string, argv []string, environment []string) (string, error) {
	command := exec.Command("git", argv...)
	command.Dir = directory
	if environment != nil {
		command.Env = environment
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s: %w", strings.Join(argv, " "), strings.TrimSpace(string(output)), err)
	}
	return strings.TrimSpace(string(output)), nil
}

func (suite Suite) resolve(relative string) (string, error) {
	if !safeRelative(relative) {
		return "", errors.New("unsafe benchmark path")
	}
	absolute := filepath.Join(suite.root, filepath.FromSlash(relative))
	if !strings.HasPrefix(absolute, suite.root+string(filepath.Separator)) {
		return "", errors.New("benchmark path escapes suite root")
	}
	return absolute, nil
}

func findGroup(groups []Group, id string) (Group, bool) {
	for _, group := range groups {
		if group.ID == id {
			return group, true
		}
	}
	return Group{}, false
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

func validCategory(value Category) bool {
	return value == CategoryBug || value == CategoryFeature || value == CategoryRefactor || value == CategoryEnvironment || value == CategoryRecovery || value == CategoryMultiModule
}

func safeID(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && !strings.ContainsAny(value, "/\\\r\n\x00")
}

func safeRelative(value string) bool {
	if value == "" || filepath.IsAbs(value) || filepath.Clean(value) != value || value == "." || value == ".." || strings.HasPrefix(value, ".."+string(filepath.Separator)) {
		return false
	}
	return !strings.ContainsRune(value, '\x00')
}

func validHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func bytesHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

type ResultReport struct {
	ProtocolVersion string      `json:"protocol_version"`
	SuiteHash       string      `json:"suite_hash"`
	Runs            []RunResult `json:"runs"`
	GeneratedAt     string      `json:"generated_at"`
}

func NewResultReport(suiteHash string, runs []RunResult, generatedAt string) (ResultReport, error) {
	value := ResultReport{ProtocolVersion: ResultVersion, SuiteHash: suiteHash, Runs: append([]RunResult(nil), runs...), GeneratedAt: generatedAt}
	if !validHex(suiteHash, 64) {
		return ResultReport{}, errors.New("invalid suite hash")
	}
	if _, err := time.Parse(time.RFC3339Nano, generatedAt); err != nil || len(runs) == 0 {
		return ResultReport{}, errors.New("invalid benchmark result header")
	}
	for index := range value.Runs {
		value.Runs[index].FalseCompleted = value.Runs[index].RunnerClaimedCompleted && !value.Runs[index].FinalAcceptancePass
		if err := validateRunResult(value.Runs[index]); err != nil {
			return ResultReport{}, err
		}
	}
	sort.Slice(value.Runs, func(i, j int) bool {
		left, right := value.Runs[i], value.Runs[j]
		if left.TaskID != right.TaskID {
			return left.TaskID < right.TaskID
		}
		if left.GroupID != right.GroupID {
			return left.GroupID < right.GroupID
		}
		return left.Run < right.Run
	})
	return value, nil
}

func (value ResultReport) Render() ([]byte, []byte, error) {
	jsonBytes, err := canonical.Marshal(value)
	if err != nil {
		return nil, nil, err
	}
	var out bytes.Buffer
	out.WriteString("# xgoal Benchmark Results\n\n")
	fmt.Fprintf(&out, "Suite: `%s`  \nGenerated: `%s`\n\n", value.SuiteHash, value.GeneratedAt)
	out.WriteString("| Task | Group | Run | Acceptance | First Attempt | False Completed | Regressions | Recovery | No Progress | Attempts | Human | Wall ms |\n| --- | --- | ---: | --- | --- | --- | ---: | --- | --- | ---: | ---: | ---: |\n")
	for _, run := range value.Runs {
		status := "FAIL"
		if run.FinalAcceptancePass {
			status = "PASS"
		}
		fmt.Fprintf(&out, "| %s | %s | %d | %s | %t | %t | %d | %t | %s | %d | %d | %s |\n", run.TaskID, run.GroupID, run.Run, status, run.FirstAttemptPass, run.FalseCompleted, run.RegressionFailures, run.RecoverySuccess, noProgressText(run), run.Attempts, run.HumanInterventions, metricText(run.WallTime))
	}
	out.WriteString("\nFailures and human interventions are retained; this file does not upload results.\n")
	return jsonBytes, out.Bytes(), nil
}

func validateMetric(value Metric) error {
	if value.Unit == "" || value.Value < 0 || (!value.Known && value.Value != 0) {
		return errors.New("invalid benchmark metric")
	}
	return nil
}

func validateRunResult(value RunResult) error {
	if !safeID(value.TaskID) || !safeID(value.GroupID) || value.Run <= 0 || value.Attempts < 0 || value.HumanInterventions < 0 || value.RegressionFailures < 0 || value.NoProgressAttempts < 0 || value.NoProgressAttempts > value.Attempts {
		return errors.New("invalid benchmark run identity or counters")
	}
	started, startErr := time.Parse(time.RFC3339Nano, value.StartedAt)
	completed, completeErr := time.Parse(time.RFC3339Nano, value.CompletedAt)
	if startErr != nil || completeErr != nil || completed.Before(started) {
		return errors.New("invalid benchmark run timestamps")
	}
	if err := validateMetric(value.WallTime); err != nil {
		return err
	}
	if value.WallTime.Known && value.WallTime.Value != completed.Sub(started).Milliseconds() {
		return errors.New("benchmark wall time does not match timestamps")
	}
	return nil
}

func noProgressText(value RunResult) string {
	if value.Attempts == 0 {
		return "not-applicable"
	}
	return fmt.Sprintf("%d/%d", value.NoProgressAttempts, value.Attempts)
}

func metricText(value Metric) string {
	if !value.Known {
		return "unknown"
	}
	return strconv.FormatInt(value.Value, 10)
}
