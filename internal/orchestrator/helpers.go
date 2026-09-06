package orchestrator

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/monshunter/xgoal/internal/config"
	"github.com/monshunter/xgoal/internal/domain"
	"github.com/monshunter/xgoal/internal/goalcompile"
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
	profile, _, err := engine.config.SelectProfile(string(role), "")
	return profile, err
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
