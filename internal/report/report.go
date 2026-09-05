// Package report defines the immutable, deterministic final-report projection.
package report

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/monshunter/xgoal/internal/canonical"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/scenario"
)

const ProtocolVersion = "xgoal.final-report/v1"

type Report struct {
	Scenarios       []scenario.Manifest `json:"scenarios,omitempty"`
	ProtocolVersion string              `json:"protocol_version"`
	Goal            GoalTrace           `json:"goal"`
	Work            []WorkTrace         `json:"work"`
	Attempts        []AttemptTrace      `json:"attempts"`
	Final           FinalTrace          `json:"final"`
	Criteria        []CriterionTrace    `json:"criteria"`
	Validators      []ValidatorTrace    `json:"validators"`
	Gates           []GateTrace         `json:"gates"`
	Findings        []FindingTrace      `json:"findings"`
	Execution       []ExecutionMetric   `json:"execution_metrics"`
	Limitations     []Statement         `json:"limitations"`
	CancelledScopes []Statement         `json:"cancelled_scopes"`
	Timestamps      TimestampTrace      `json:"timestamps"`
}

type GoalTrace struct {
	ID           string           `json:"id"`
	Raw          string           `json:"raw"`
	Revision     int64            `json:"revision"`
	RevisionHash string           `json:"revision_hash"`
	ConfigHash   string           `json:"config_hash"`
	CreatedBy    string           `json:"created_by"`
	Authority    domain.Authority `json:"authority"`
}

type WorkTrace struct {
	ID        string           `json:"id"`
	State     string           `json:"state"`
	Title     string           `json:"title"`
	Required  bool             `json:"required"`
	Authority domain.Authority `json:"authority"`
}

type AttemptTrace struct {
	ID         string           `json:"id"`
	WorkID     string           `json:"work_id"`
	Role       string           `json:"role"`
	Provider   string           `json:"provider"`
	State      string           `json:"state"`
	PacketHash string           `json:"packet_hash"`
	ResultTree string           `json:"result_tree"`
	Authority  domain.Authority `json:"authority"`
}

type FinalTrace struct {
	Commit        string           `json:"commit"`
	Tree          string           `json:"tree"`
	EvidenceSetID string           `json:"evidence_set_id"`
	Scope         []string         `json:"scope"`
	Decisions     []Statement      `json:"decisions"`
	Authority     domain.Authority `json:"authority"`
}

type CriterionTrace struct {
	ScenarioIDs  []string         `json:"scenario_ids,omitempty"`
	ID           string           `json:"id"`
	Description  string           `json:"description"`
	Status       string           `json:"status"`
	EvidenceIDs  []string         `json:"evidence_ids"`
	ValidatorIDs []string         `json:"validator_ids"`
	Authority    domain.Authority `json:"authority"`
}

type ValidatorTrace struct {
	ID           string           `json:"id"`
	Command      []string         `json:"command"`
	ReceiptHash  string           `json:"receipt_hash"`
	Result       string           `json:"result"`
	Reproduction []string         `json:"reproduction"`
	Flaky        bool             `json:"flaky"`
	Authority    domain.Authority `json:"authority"`
}

type GateTrace struct {
	ID        string           `json:"id"`
	State     string           `json:"state"`
	Decision  string           `json:"decision"`
	Reason    string           `json:"reason"`
	Authority domain.Authority `json:"authority"`
}

type FindingTrace struct {
	ID        string           `json:"id"`
	Severity  string           `json:"severity"`
	State     string           `json:"state"`
	Claim     string           `json:"claim"`
	Basis     string           `json:"basis"`
	Authority domain.Authority `json:"authority"`
}

type ExecutionMetric struct {
	Name      string           `json:"name"`
	Unit      string           `json:"unit"`
	Known     bool             `json:"known"`
	Value     int64            `json:"value"`
	Authority domain.Authority `json:"authority"`
}

type Statement struct {
	Text      string           `json:"text"`
	Authority domain.Authority `json:"authority"`
}

type TimestampTrace struct {
	StartedAt   string           `json:"started_at"`
	CompletedAt string           `json:"completed_at"`
	Authority   domain.Authority `json:"authority"`
}

type Artifact struct {
	JSON         []byte
	Markdown     []byte
	JSONHash     string
	MarkdownHash string
	ReportHash   string
}

type identity struct {
	ProtocolVersion string `json:"protocol_version"`
	GoalRevision    string `json:"goal_revision_hash"`
	Config          string `json:"config_hash"`
	FinalTree       string `json:"final_tree"`
	EvidenceSetID   string `json:"evidence_set_id"`
	JSONHash        string `json:"json_hash"`
	MarkdownHash    string `json:"markdown_hash"`
}

func Render(input Report) (Artifact, error) {
	normalized := normalize(input)
	if err := normalized.validate(); err != nil {
		return Artifact{}, err
	}
	jsonBytes, err := canonical.Marshal(normalized)
	if err != nil {
		return Artifact{}, fmt.Errorf("encode final report JSON: %w", err)
	}
	markdown := renderMarkdown(normalized)
	jsonHash := bytesHash(jsonBytes)
	markdownHash := bytesHash(markdown)
	reportHash, err := canonical.Hash("final-report", ProtocolVersion, identity{
		ProtocolVersion: ProtocolVersion,
		GoalRevision:    normalized.Goal.RevisionHash,
		Config:          normalized.Goal.ConfigHash,
		FinalTree:       normalized.Final.Tree,
		EvidenceSetID:   normalized.Final.EvidenceSetID,
		JSONHash:        jsonHash,
		MarkdownHash:    markdownHash,
	})
	if err != nil {
		return Artifact{}, err
	}
	return Artifact{JSON: jsonBytes, Markdown: markdown, JSONHash: jsonHash, MarkdownHash: markdownHash, ReportHash: reportHash}, nil
}

// VerifyArtifact strictly decodes a persisted report and proves both projections and all hashes.
func VerifyArtifact(artifact Artifact) (Report, error) {
	decoder := json.NewDecoder(bytes.NewReader(artifact.JSON))
	decoder.DisallowUnknownFields()
	var value Report
	if err := decoder.Decode(&value); err != nil {
		return Report{}, fmt.Errorf("decode final report JSON: %w", err)
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Report{}, err
	}
	rendered, err := Render(value)
	if err != nil {
		return Report{}, err
	}
	if !bytes.Equal(rendered.JSON, artifact.JSON) || !bytes.Equal(rendered.Markdown, artifact.Markdown) ||
		rendered.JSONHash != artifact.JSONHash || rendered.MarkdownHash != artifact.MarkdownHash || rendered.ReportHash != artifact.ReportHash {
		return Report{}, ErrArtifactMismatch
	}
	return normalize(value), nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("final report JSON contains multiple values")
		}
		return fmt.Errorf("decode final report JSON suffix: %w", err)
	}
	return nil
}

func (value Report) validate() error {
	seenScenarios := map[string]string{}
	for _, m := range value.Scenarios {
		if err := m.Validate(); err != nil {
			return err
		}
		if seenScenarios[m.Scenario.ID] != "" || m.GoalRevisionHash != value.Goal.RevisionHash || m.ConfigHash != value.Goal.ConfigHash || m.TreeHash != value.Final.Tree {
			return errors.New("report scenario identity or final binding mismatch")
		}
		seenScenarios[m.Scenario.ID] = m.EvidenceID
	}
	for _, criterion := range value.Criteria {
		for _, id := range criterion.ScenarioIDs {
			if seenScenarios[id] == "" || !containsString(criterion.EvidenceIDs, seenScenarios[id]) {
				return errors.New("report criterion lacks bound scenario evidence")
			}
		}
	}
	if value.ProtocolVersion != ProtocolVersion || strings.TrimSpace(value.Goal.ID) == "" || strings.TrimSpace(value.Goal.Raw) == "" || value.Goal.Revision <= 0 ||
		!validHash(value.Goal.RevisionHash, 64) || !validHash(value.Goal.ConfigHash, 64) || strings.TrimSpace(value.Goal.CreatedBy) == "" || !value.Goal.Authority.Valid() {
		return errors.New("invalid final report goal binding")
	}
	if !validHash(value.Final.Commit, 40, 64) || !validHash(value.Final.Tree, 40, 64) || strings.TrimSpace(value.Final.EvidenceSetID) == "" || len(value.Final.Scope) == 0 || !value.Final.Authority.Valid() {
		return errors.New("invalid final report integration binding")
	}
	if len(value.Work) == 0 || len(value.Criteria) == 0 || len(value.Validators) == 0 {
		return errors.New("final report requires work, criteria, and validators")
	}
	if err := uniqueIDs("work", len(value.Work), func(index int) (string, bool) {
		item := value.Work[index]
		return item.ID, item.ID != "" && item.State != "" && item.Title != "" && item.Authority.Valid()
	}); err != nil {
		return err
	}
	if err := uniqueIDs("attempt", len(value.Attempts), func(index int) (string, bool) {
		item := value.Attempts[index]
		state := domain.AttemptState(item.State)
		terminal := state == domain.AttemptSucceeded || state == domain.AttemptFailed || state == domain.AttemptTimedOut || state == domain.AttemptInterrupted || state == domain.AttemptInvalidOutput || state == domain.AttemptQuarantined
		resultTreeValid := item.ResultTree == "" || validHash(item.ResultTree, 40, 64)
		if state == domain.AttemptSucceeded {
			resultTreeValid = validHash(item.ResultTree, 40, 64)
		}
		return item.ID, item.ID != "" && item.WorkID != "" && item.Role != "" && item.Provider != "" && terminal && validHash(item.PacketHash, 64) && resultTreeValid && item.Authority.Valid()
	}); err != nil {
		return err
	}
	if err := uniqueIDs("criterion", len(value.Criteria), func(index int) (string, bool) {
		item := value.Criteria[index]
		return item.ID, item.ID != "" && item.Description != "" && (item.Status == "PASS" || item.Status == "FAIL" || item.Status == "NOT_RUN") && len(item.EvidenceIDs) > 0 && item.Authority.Valid()
	}); err != nil {
		return err
	}
	if err := uniqueIDs("validator", len(value.Validators), func(index int) (string, bool) {
		item := value.Validators[index]
		return item.ID, item.ID != "" && len(item.Command) > 0 && validHash(item.ReceiptHash, 64) && item.Result != "" && len(item.Reproduction) > 0 && item.Authority.Valid()
	}); err != nil {
		return err
	}
	for _, gate := range value.Gates {
		if gate.ID == "" || gate.State == "" || !gate.Authority.Valid() {
			return errors.New("invalid gate trace")
		}
	}
	for _, finding := range value.Findings {
		if finding.ID == "" || finding.Severity == "" || finding.State == "" || finding.Claim == "" || finding.Basis == "" || !finding.Authority.Valid() {
			return errors.New("invalid finding trace")
		}
	}
	for _, metric := range value.Execution {
		if metric.Name == "" || metric.Unit == "" || metric.Value < 0 || (!metric.Known && metric.Value != 0) || !metric.Authority.Valid() {
			return errors.New("invalid execution metric")
		}
	}
	for _, statements := range [][]Statement{value.Final.Decisions, value.Limitations, value.CancelledScopes} {
		for _, statement := range statements {
			if strings.TrimSpace(statement.Text) == "" || !statement.Authority.Valid() {
				return errors.New("invalid report statement")
			}
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, value.Timestamps.StartedAt); err != nil || !value.Timestamps.Authority.Valid() {
		return errors.New("invalid report start timestamp")
	}
	completed, err := time.Parse(time.RFC3339Nano, value.Timestamps.CompletedAt)
	if err != nil {
		return errors.New("invalid report completion timestamp")
	}
	started, _ := time.Parse(time.RFC3339Nano, value.Timestamps.StartedAt)
	if completed.Before(started) {
		return errors.New("report completion precedes start")
	}
	return nil
}

func normalize(value Report) Report {
	value.Scenarios = append([]scenario.Manifest(nil), value.Scenarios...)
	sort.Slice(value.Scenarios, func(i, j int) bool { return value.Scenarios[i].Scenario.ID < value.Scenarios[j].Scenario.ID })
	value.Work = append([]WorkTrace(nil), value.Work...)
	value.Attempts = append([]AttemptTrace(nil), value.Attempts...)
	value.Criteria = append([]CriterionTrace(nil), value.Criteria...)
	value.Validators = append([]ValidatorTrace(nil), value.Validators...)
	value.Gates = append([]GateTrace(nil), value.Gates...)
	value.Findings = append([]FindingTrace(nil), value.Findings...)
	value.Execution = append([]ExecutionMetric(nil), value.Execution...)
	value.Final.Scope = sortedStrings(value.Final.Scope)
	value.Final.Decisions = sortedStatements(value.Final.Decisions)
	value.Limitations = sortedStatements(value.Limitations)
	value.CancelledScopes = sortedStatements(value.CancelledScopes)
	sort.Slice(value.Work, func(i, j int) bool { return value.Work[i].ID < value.Work[j].ID })
	sort.Slice(value.Attempts, func(i, j int) bool { return value.Attempts[i].ID < value.Attempts[j].ID })
	sort.Slice(value.Criteria, func(i, j int) bool { return value.Criteria[i].ID < value.Criteria[j].ID })
	for index := range value.Criteria {
		value.Criteria[index].ScenarioIDs = sortedStrings(value.Criteria[index].ScenarioIDs)
		value.Criteria[index].EvidenceIDs = sortedStrings(value.Criteria[index].EvidenceIDs)
		value.Criteria[index].ValidatorIDs = sortedStrings(value.Criteria[index].ValidatorIDs)
	}
	sort.Slice(value.Validators, func(i, j int) bool { return value.Validators[i].ID < value.Validators[j].ID })
	sort.Slice(value.Gates, func(i, j int) bool { return value.Gates[i].ID < value.Gates[j].ID })
	sort.Slice(value.Findings, func(i, j int) bool { return value.Findings[i].ID < value.Findings[j].ID })
	sort.Slice(value.Execution, func(i, j int) bool { return value.Execution[i].Name < value.Execution[j].Name })
	return value
}

func renderMarkdown(value Report) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "# xgoal Final Report: %s\n\n", md(value.Goal.ID))
	fmt.Fprintf(&out, "Protocol: `%s`  \nGoal Revision: `%s`  \nConfig: `%s`  \nFinal Commit: `%s`  \nFinal Tree: `%s`  \nEvidence Set: `%s`\n\n", value.ProtocolVersion, value.Goal.RevisionHash, value.Goal.ConfigHash, value.Final.Commit, value.Final.Tree, md(value.Final.EvidenceSetID))
	fmt.Fprintf(&out, "## Goal\n\n%s\n\nCreator: `%s` (%s)\n\n", md(value.Goal.Raw), md(value.Goal.CreatedBy), value.Goal.Authority)
	out.WriteString("## Work and Attempts\n\n")
	for _, work := range value.Work {
		fmt.Fprintf(&out, "- `%s` — %s — `%s` — required=%t (%s)\n", md(work.ID), md(work.Title), md(work.State), work.Required, work.Authority)
	}
	for _, attempt := range value.Attempts {
		fmt.Fprintf(&out, "  - attempt `%s`, work `%s`, %s/%s, `%s`, tree `%s` (%s)\n", md(attempt.ID), md(attempt.WorkID), md(attempt.Role), md(attempt.Provider), md(attempt.State), attempt.ResultTree, attempt.Authority)
	}
	out.WriteString("\n## Acceptance Criteria\n\n| ID | Status | Evidence | Validators | Authority |\n| --- | --- | --- | --- | --- |\n")
	for _, criterion := range value.Criteria {
		fmt.Fprintf(&out, "| %s | %s | %s | %s | %s |\n", md(criterion.ID), md(criterion.Status), md(strings.Join(criterion.EvidenceIDs, ", ")), md(strings.Join(criterion.ValidatorIDs, ", ")), criterion.Authority)
		fmt.Fprintf(&out, "\n%s: %s\n", md(criterion.ID), md(criterion.Description))
	}
	out.WriteString("\n## Validators\n\n")
	for _, validator := range value.Validators {
		fmt.Fprintf(&out, "- `%s`: `%s` → `%s`; flaky=%t; receipt `%s`; reproduce: `%s` (%s)\n", md(validator.ID), md(strings.Join(validator.Command, " ")), md(validator.Result), validator.Flaky, validator.ReceiptHash, md(strings.Join(validator.Reproduction, " ")), validator.Authority)
	}
	if len(value.Scenarios) > 0 {
		out.WriteString("\n## Scenario Artifacts\n\n")
		for _, m := range value.Scenarios {
			fmt.Fprintf(&out, "- `%s`: %s; evidence `%s`; environment `%s`; manifest `%s`\n", md(m.Scenario.ID), md(m.Scenario.Description), md(m.EvidenceID), md(m.EnvironmentID), m.Hash)
			for _, f := range m.Files {
				fmt.Fprintf(&out, "  - `%s`, %d bytes, SHA-256 `%s`\n", md(f.Path), f.Size, f.SHA256)
			}
		}
	}
	out.WriteString("\n## Gates and Findings\n\n")
	if len(value.Gates) == 0 && len(value.Findings) == 0 {
		out.WriteString("None.\n")
	}
	for _, gate := range value.Gates {
		fmt.Fprintf(&out, "- Gate `%s`: `%s` / `%s` — %s (%s)\n", md(gate.ID), md(gate.State), md(gate.Decision), md(gate.Reason), gate.Authority)
	}
	for _, finding := range value.Findings {
		fmt.Fprintf(&out, "- Finding `%s`: `%s` / `%s` — %s; basis: %s (%s)\n", md(finding.ID), md(finding.Severity), md(finding.State), md(finding.Claim), md(finding.Basis), finding.Authority)
	}
	out.WriteString("\n## Execution Metrics\n\n| Metric | Value | Unit | Authority |\n| --- | ---: | --- | --- |\n")
	for _, metric := range value.Execution {
		metricValue := "unknown"
		if metric.Known {
			metricValue = strconv.FormatInt(metric.Value, 10)
		}
		fmt.Fprintf(&out, "| %s | %s | %s | %s |\n", md(metric.Name), metricValue, md(metric.Unit), metric.Authority)
	}
	renderStatements(&out, "Design Decisions", value.Final.Decisions)
	renderStatements(&out, "Limitations", value.Limitations)
	renderStatements(&out, "Cancelled Scope", value.CancelledScopes)
	fmt.Fprintf(&out, "\n## Time\n\nStarted: `%s`  \nCompleted: `%s` (%s)\n", value.Timestamps.StartedAt, value.Timestamps.CompletedAt, value.Timestamps.Authority)
	return out.Bytes()
}

func renderStatements(out *bytes.Buffer, title string, values []Statement) {
	fmt.Fprintf(out, "\n## %s\n\n", title)
	if len(values) == 0 {
		out.WriteString("None.\n")
		return
	}
	for _, value := range values {
		fmt.Fprintf(out, "- %s (%s)\n", md(value.Text), value.Authority)
	}
}

func md(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "`", "\\`")
	value = strings.ReplaceAll(value, "\r", " ")
	return strings.ReplaceAll(value, "\n", " ")
}

func uniqueIDs(kind string, count int, item func(int) (string, bool)) error {
	seen := make(map[string]struct{}, count)
	for index := range count {
		id, valid := item(index)
		if !valid {
			return fmt.Errorf("invalid %s trace", kind)
		}
		if _, exists := seen[id]; exists {
			return fmt.Errorf("duplicate %s %q", kind, id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func sortedStrings(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}

func sortedStatements(values []Statement) []Statement {
	result := append([]Statement(nil), values...)
	sort.Slice(result, func(i, j int) bool {
		if result[i].Text == result[j].Text {
			return result[i].Authority < result[j].Authority
		}
		return result[i].Text < result[j].Text
	})
	return result
}

func validHash(value string, lengths ...int) bool {
	for _, length := range lengths {
		if len(value) == length {
			_, err := hex.DecodeString(value)
			return err == nil
		}
	}
	return false
}

func bytesHash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
