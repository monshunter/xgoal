package orchestrator

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/goalcompile"
	"github.com/monshunter/xgoal/internal/review"
	"github.com/monshunter/xgoal/internal/store/sqlite"
)

type frozenContract struct {
	ProtocolVersion string               `json:"protocol_version"`
	Contract        goalcompile.Contract `json:"contract"`
	ConfigHash      string               `json:"config_hash"`
	CreatedBy       string               `json:"created_by"`
	Mode            string               `json:"mode"`
}

func decodeFrozenContract(value []byte) (frozenContract, error) {
	decoder := json.NewDecoder(bytes.NewReader(value))
	decoder.DisallowUnknownFields()
	var result frozenContract
	if err := decoder.Decode(&result); err != nil {
		return frozenContract{}, fmt.Errorf("decode frozen Goal Contract: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return frozenContract{}, errors.New("frozen Goal Contract contains trailing data")
	}
	if result.ProtocolVersion != goalcompile.ContractVersion || result.ConfigHash == "" || result.CreatedBy == "" || (result.Mode != "fast" && result.Mode != "standard") {
		return frozenContract{}, errors.New("frozen Goal Contract provenance is incomplete")
	}
	if err := result.Contract.Validate(); err != nil {
		return frozenContract{}, err
	}
	return result, nil
}

func (engine *Engine) implementationProfile(role domain.Role) (config.Agent, error) {
	for _, configured := range engine.config.Agents {
		if hasRole(configured, role) {
			return configured, nil
		}
	}
	return config.Agent{}, fmt.Errorf("no trusted Agent Profile supports role %q", role)
}

func (engine *Engine) reviewerProfile(implementer config.Agent) (config.Agent, review.Adapter, error) {
	candidates := make([]config.Agent, 0)
	for _, configured := range engine.config.Agents {
		if hasRole(configured, domain.RoleReviewer) && engine.reviewers[configured.ID] != nil {
			candidates = append(candidates, configured)
		}
	}
	if len(candidates) == 0 {
		return config.Agent{}, nil, errors.New("no trusted Reviewer Profile is available")
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		leftDifferent := candidates[i].Adapter != implementer.Adapter
		rightDifferent := candidates[j].Adapter != implementer.Adapter
		if engine.config.Review.PreferDifferentProvider && leftDifferent != rightDifferent {
			return leftDifferent
		}
		leftProfile := candidates[i].ID != implementer.ID
		rightProfile := candidates[j].ID != implementer.ID
		if leftProfile != rightProfile {
			return leftProfile
		}
		return candidates[i].ID < candidates[j].ID
	})
	selected := candidates[0]
	return selected, engine.reviewers[selected.ID], nil
}

func hasRole(profile config.Agent, role domain.Role) bool {
	for _, configured := range profile.Roles {
		if configured == string(role) {
			return true
		}
	}
	return false
}

func randomID(prefix string) (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(value), nil
}

func event(kind, actor string, payload any) sqlite.EventInput {
	return sqlite.EventInput{Type: kind, ActorType: actor, Payload: payload}
}
