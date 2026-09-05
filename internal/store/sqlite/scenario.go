package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"

	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/goalcompile"
	finalreport "github.com/monshunter/xgoal/internal/report"
	"github.com/monshunter/xgoal/internal/scenario"
)

func verifyFrozenScenarioMapping(contractJSON []byte, report finalreport.Report) error {
	var frozen struct {
		Contract goalcompile.Contract `json:"contract"`
	}
	if err := json.Unmarshal(contractJSON, &frozen); err != nil {
		return err
	}
	byID := map[string]finalreport.CriterionTrace{}
	for _, criterion := range report.Criteria {
		byID[criterion.ID] = criterion
	}
	expectedScenarios := map[string]bool{}
	for _, criterion := range frozen.Contract.AcceptanceCriteria {
		actual, exists := byID[criterion.ID]
		if !exists || !slices.Equal(sortedCopy(criterion.ScenarioIDs), sortedCopy(actual.ScenarioIDs)) {
			return errors.New("report scenario mapping differs from frozen criterion")
		}
		if len(criterion.ScenarioIDs) > 0 && !slices.Equal(sortedCopy(criterion.Validators), sortedCopy(actual.ValidatorIDs)) {
			return errors.New("report scenario assertions differ from frozen criterion")
		}
		for _, id := range criterion.ScenarioIDs {
			expectedScenarios[id] = true
		}
	}
	if len(expectedScenarios) != len(report.Scenarios) {
		return errors.New("report omits or adds frozen scenario manifests")
	}
	for _, m := range report.Scenarios {
		if !expectedScenarios[m.Scenario.ID] {
			return errors.New("report contains an unfrozen scenario")
		}
	}
	return nil
}

func verifyScenarioEvidence(ctx context.Context, tx *sql.Tx, goalID string, report finalreport.Report, set evidence.Set) error {
	for _, criterion := range report.Criteria {
		if len(criterion.ScenarioIDs) == 0 {
			continue
		}
		ids, err := json.Marshal(criterion.EvidenceIDs)
		if err != nil {
			return err
		}
		for _, validatorID := range criterion.ValidatorIDs {
			var count int
			err := tx.QueryRowContext(ctx, `SELECT count(*) FROM evidence_records e JOIN validator_runs r ON r.receipt_hash=e.receipt_hash JOIN validator_definitions d ON d.definition_hash=r.definition_hash JOIN evidence_set_members sm ON sm.evidence_id=e.id WHERE sm.evidence_set_id=? AND e.id IN (SELECT value FROM json_each(?)) AND d.id=? AND r.result='PASSED' AND r.goal_revision_hash=? AND r.config_hash=? AND r.tree_hash=?`, set.ID, string(ids), validatorID, set.GoalRevisionHash, set.ConfigHash, set.TreeHash).Scan(&count)
			if err != nil {
				return err
			}
			if count != 1 {
				return errors.New("frozen scenario criterion lacks a passing business receipt")
			}
		}
	}
	for _, m := range report.Scenarios {
		if !slices.Contains(set.EvidenceIDs, m.EvidenceID) {
			return errors.New("scenario evidence is absent from final set")
		}
		record, err := readEvidence(ctx, tx, m.EvidenceID)
		if err != nil {
			return err
		}
		definitionHash, err := scenario.DefinitionHash(m.Scenario)
		if err != nil {
			return err
		}
		if record.State != domain.EvidenceCurrent || record.Evidence.Kind != "SCENARIO" || record.Evidence.SubjectID != goalID || record.Evidence.Producer != "kernel/scenario/"+m.Scenario.ID || record.Evidence.Authority != domain.AuthorityDeterministic || record.Evidence.GoalRevisionHash != m.GoalRevisionHash || record.Evidence.ConfigHash != m.ConfigHash || record.Evidence.TreeHash != m.TreeHash || record.Evidence.PayloadHash != m.Hash || record.DefinitionHash != definitionHash || record.EnvironmentHash != m.EnvironmentHash {
			return errors.New("scenario manifest and current Evidence have different bindings")
		}
		for validatorID, hash := range m.ReceiptHashes {
			var count int
			err := tx.QueryRowContext(ctx, `SELECT count(*) FROM validator_runs r JOIN validator_definitions d ON d.definition_hash=r.definition_hash JOIN evidence_records e ON e.receipt_hash=r.receipt_hash JOIN evidence_set_members sm ON sm.evidence_id=e.id WHERE sm.evidence_set_id=? AND r.receipt_hash=? AND d.id=? AND r.result='PASSED' AND r.goal_revision_hash=? AND r.config_hash=? AND r.tree_hash=? AND r.environment_hash=?`, set.ID, hash, validatorID, m.GoalRevisionHash, m.ConfigHash, m.TreeHash, m.EnvironmentHash).Scan(&count)
			if err != nil {
				return err
			}
			if count != 1 {
				return errors.New("scenario assertion lacks a passing receipt in the same final Evidence Set")
			}
		}
	}
	return nil
}
