package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/environment"
	"github.com/monshunter/xgoal/internal/evidence"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/protocol"
	"github.com/monshunter/xgoal/internal/scenario"
	"github.com/monshunter/xgoal/internal/validator"
)

func (engine *Engine) finalScenarios(frozen frozenContract, registry *validator.Registry) ([]config.Scenario, []string, error) {
	trusted := map[string]bool{}
	for _, definition := range registry.Definitions() {
		trusted[definition.ID] = true
	}
	capabilities := engine.config.ValidationCapabilities()
	for _, g := range frozen.Contract.GeneratedValidators {
		capabilities.Validators = append(capabilities.Validators, config.ValidatorCapability{ID: g.ID, Description: g.Description, Type: "command", Coverage: "generated", Phases: []string{"change", "final"}})
	}
	if err := goalcompile.ValidateCoverage(frozen.Contract, trusted, capabilities); err != nil {
		return nil, nil, err
	}
	var ids []string
	for _, criterion := range frozen.Contract.AcceptanceCriteria {
		ids = append(ids, criterion.ScenarioIDs...)
	}
	var selected []config.Scenario
	var services []string
	for _, id := range uniqueSorted(ids) {
		for _, configured := range engine.config.Scenarios {
			if configured.ID == id {
				selected = append(selected, configured)
				services = append(services, configured.Services...)
				break
			}
		}
	}
	return selected, uniqueSorted(services), nil
}

func (engine *Engine) sealScenarios(ctx context.Context, goalID string, revision domain.GoalRevision, tree string, handle environment.Handle, selected []config.Scenario, runs []validationEvidence) ([]scenario.Manifest, error) {
	byValidator := map[string]validationEvidence{}
	for _, run := range runs {
		byValidator[run.Validator] = run
	}
	var manifests []scenario.Manifest
	for _, spec := range selected {
		id, err := randomID("evidence_scenario")
		if err != nil {
			return nil, err
		}
		m := scenario.Manifest{EvidenceID: id, Scenario: spec, GoalRevisionHash: revision.Hash, ConfigHash: engine.configHash, TreeHash: tree, EnvironmentID: handle.ID, ReceiptHashes: map[string]string{}}
		for _, validatorID := range spec.Validators {
			run, exists := byValidator[validatorID]
			if !exists || run.Receipt.Result != protocol.CommandPassed || run.Receipt.GoalRevisionHash != revision.Hash || run.Receipt.TreeHash != tree || run.Receipt.ConfigHash != engine.configHash {
				return nil, fmt.Errorf("scenario %q lacks a current passing receipt for %q", spec.ID, validatorID)
			}
			if m.EnvironmentHash != "" && m.EnvironmentHash != run.Receipt.EnvironmentHash {
				return nil, errors.New("scenario assertions used different environments")
			}
			m.EnvironmentHash = run.Receipt.EnvironmentHash
			m.ReceiptHashes[validatorID] = run.Hash
		}
		m, err = scenario.Seal(ctx, engine.runtimeRoot, filepath.Join(handle.Root, "scenario"), m)
		if err != nil {
			return nil, err
		}
		definitionHash, err := scenario.DefinitionHash(spec)
		if err != nil {
			return nil, err
		}
		record := evidence.Record{Evidence: protocol.Evidence{ProtocolVersion: protocol.EvidenceVersion, ID: id, Kind: "SCENARIO", SubjectID: goalID, Producer: "kernel/scenario/" + spec.ID, Authority: domain.AuthorityDeterministic, GoalRevisionHash: revision.Hash, ConfigHash: engine.configHash, TreeHash: tree, PayloadHash: m.Hash, State: domain.EvidenceCurrent, CreatedAt: time.Now().UTC()}, DefinitionHash: definitionHash, EnvironmentHash: m.EnvironmentHash}
		if err := engine.store.AppendEvidence(ctx, record); err != nil {
			return nil, err
		}
		manifests = append(manifests, m)
	}
	return manifests, nil
}
