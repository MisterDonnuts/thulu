package types

import (
	"strings"

	"github.com/MisterDonnuts/thulu/internal/apperr"
)

const (
	TokenOptimizerOff     = "off"
	TokenOptimizerSuggest = "suggest"
	TokenOptimizerEnforce = "enforce"
)

// Must match the `when:` value in the token-optimizer system skill's definition.
const SkillWhenTokenOptimizer = "token_optimizer" //nolint:gosec // G101: a skill condition name, not a credential

type TokenOptimizer struct {
	Level  string `json:"level,omitempty"`
	Engine string `json:"engine,omitempty"`
	Model  string `json:"model,omitempty"`
}

func (t TokenOptimizer) IsZero() bool {
	return t.Level == "" && t.Engine == "" && t.Model == ""
}

func (t TokenOptimizer) On() bool {
	return t.Level == TokenOptimizerSuggest || t.Level == TokenOptimizerEnforce
}

func (t TokenOptimizer) Normalize() TokenOptimizer {
	t.Level = strings.ToLower(strings.TrimSpace(t.Level))
	if t.Level == "" || t.Level == TokenOptimizerOff {
		return TokenOptimizer{}
	}
	t.Engine, t.Model = strings.TrimSpace(t.Engine), strings.TrimSpace(t.Model)
	return t
}

// Definitions saved before the worker moved to the agent carry a level without engine or model;
// they load as off rather than as a policy no save would accept.
func (t TokenOptimizer) OrOff() TokenOptimizer {
	t = t.Normalize()
	if !t.On() || t.Engine == "" || t.Model == "" {
		return TokenOptimizer{}
	}
	return t
}

func (t TokenOptimizer) Validate(engineKnown func(string) bool) error {
	switch {
	case t.Level != "" && !ValidTokenOptimizerLevel(t.Level):
		return apperr.Invalid("tokenopt.errors.invalid_level", "level", t.Level)
	case !t.On():
		return nil
	case t.Engine == "":
		return apperr.Invalid("tokenopt.errors.engine_required")
	case !engineKnown(t.Engine):
		return apperr.Invalid("tokenopt.errors.unknown_engine", "engine", t.Engine)
	case t.Model == "":
		return apperr.Invalid("tokenopt.errors.model_required")
	}
	return nil
}

func ValidTokenOptimizerLevel(v string) bool {
	switch v {
	case TokenOptimizerOff, TokenOptimizerSuggest, TokenOptimizerEnforce:
		return true
	}
	return false
}

func parseTokenOptimizerLabels(labels map[string]string) TokenOptimizer {
	out := TokenOptimizer{
		Level:  strings.ToLower(strings.TrimSpace(labels[LabelTokenOptimizer])),
		Engine: labels[LabelTokenOptimizerEngine],
		Model:  labels[LabelTokenOptimizerModel],
	}
	return out.OrOff()
}

func TokenOptimizerLabels(t TokenOptimizer) map[string]string {
	out := map[string]string{}
	t = t.Normalize()
	if !t.On() {
		return out
	}
	out[LabelTokenOptimizer] = t.Level
	if t.Engine != "" {
		out[LabelTokenOptimizerEngine] = t.Engine
	}
	if t.Model != "" {
		out[LabelTokenOptimizerModel] = t.Model
	}
	return out
}

// The prefix match also catches labels of the earlier shape (per-stage, threshold_bytes) so a save
// clears them.
func IsTokenOptimizerLabel(key string) bool {
	return key == LabelTokenOptimizer || strings.HasPrefix(key, LabelTokenOptimizer+".")
}
