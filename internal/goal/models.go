package goal

import "strings"

// RoleModel picks the model one role runs on. Empty means the session's
// current model.
type RoleModel struct {
	Model     string `json:"model,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
}

func (r RoleModel) Label() string {
	if r.Model == "" {
		return "session model"
	}
	if r.Reasoning != "" {
		return r.Model + " (" + r.Reasoning + ")"
	}
	return r.Model
}

// Models assigns a model to each role, so planning and verification can use
// a strong model while workers use a cheaper or local one, or the reverse.
type Models struct {
	Planner  RoleModel `json:"planner,omitempty"`
	Worker   RoleModel `json:"worker,omitempty"`
	Verifier RoleModel `json:"verifier,omitempty"`
}

// Override returns m with every role o sets replaced.
func (m Models) Override(o Models) Models {
	pick := func(base, override RoleModel) RoleModel {
		if override.Model != "" {
			base.Model, base.Reasoning = override.Model, override.Reasoning
		} else if override.Reasoning != "" {
			base.Reasoning = override.Reasoning
		}
		return base
	}
	return Models{Planner: pick(m.Planner, o.Planner), Worker: pick(m.Worker, o.Worker), Verifier: pick(m.Verifier, o.Verifier)}
}

func (m Models) Empty() bool { return m == Models{} }

// Line renders the assignment, e.g. "planner opus (high) · worker qwen".
func (m Models) Line() string {
	return strings.Join([]string{"planner " + m.Planner.Label(), "worker " + m.Worker.Label(), "verifier " + m.Verifier.Label()}, " · ")
}

// Roles lists each role with its model, for validation.
func (m Models) Roles() map[string]RoleModel {
	return map[string]RoleModel{"planner": m.Planner, "worker": m.Worker, "verifier": m.Verifier}
}
