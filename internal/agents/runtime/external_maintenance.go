package agentruntime

import (
	"context"
	"encoding/json"
	"sort"

	agentcompaction "denova/internal/agents/context/compaction"
	agentrun "denova/internal/agents/run"
)

const maxExternalMaintenanceReceipts = 64

type externalMaintenanceState struct {
	Version  int                               `json:"version"`
	Order    []string                          `json:"order,omitempty"`
	Receipts map[string]agentcompaction.Result `json:"receipts,omitempty"`
}

func decodeExternalMaintenanceState(raw json.RawMessage, present bool) (externalMaintenanceState, error) {
	state := externalMaintenanceState{
		Version:  2,
		Receipts: make(map[string]agentcompaction.Result),
	}
	if !present || len(raw) == 0 {
		return state, nil
	}
	var probe struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return state, err
	}
	if probe.Version == 2 {
		if err := json.Unmarshal(raw, &state); err != nil {
			return state, err
		}
		if state.Receipts == nil {
			state.Receipts = make(map[string]agentcompaction.Result)
		}
		state.trim()
		return state, nil
	}
	var legacy map[string]agentcompaction.Result
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return state, err
	}
	keys := make([]string, 0, len(legacy))
	for key, result := range legacy {
		state.Receipts[key] = result
		keys = append(keys, key)
	}
	sort.Strings(keys)
	state.Order = keys
	state.trim()
	return state, nil
}

func (state *externalMaintenanceState) record(commandID string, result agentcompaction.Result) {
	if state.Receipts == nil {
		state.Receipts = make(map[string]agentcompaction.Result)
	}
	if _, exists := state.Receipts[commandID]; !exists {
		state.Order = append(state.Order, commandID)
	}
	state.Receipts[commandID] = result
	state.trim()
}

func (state *externalMaintenanceState) trim() {
	cleaned := make([]string, 0, len(state.Order))
	seen := make(map[string]bool, len(state.Order))
	for _, key := range state.Order {
		if _, ok := state.Receipts[key]; ok && !seen[key] {
			seen[key] = true
			cleaned = append(cleaned, key)
		}
	}
	for len(cleaned) > maxExternalMaintenanceReceipts {
		evict := cleaned[0]
		cleaned = cleaned[1:]
		delete(state.Receipts, evict)
	}
	state.Order = cleaned
	if len(state.Receipts) > maxExternalMaintenanceReceipts {
		extra := make([]string, 0, len(state.Receipts))
		for key := range state.Receipts {
			if !seen[key] {
				extra = append(extra, key)
			}
		}
		sort.Strings(extra)
		for _, key := range extra {
			cleaned = append(cleaned, key)
		}
		for len(cleaned) > maxExternalMaintenanceReceipts {
			evict := cleaned[0]
			cleaned = cleaned[1:]
			delete(state.Receipts, evict)
		}
		state.Order = cleaned
	}
}

// Maintain serializes explicit context maintenance with task admission and
// preserves its retry receipt in the product journal, without a Native Session.
func (control *ExternalController) Maintain(ctx context.Context, commandID string, maintain func() (agentcompaction.Result, error)) (agentcompaction.Result, error) {
	if err := agentrun.ValidateCommandID(commandID); err != nil {
		return agentcompaction.Result{}, err
	}
	control.mu.Lock()
	defer control.mu.Unlock()
	const capability = "denova.external.maintenance.v1"
	raw, found, err := control.store.Read(ctx, capability)
	if err != nil {
		return agentcompaction.Result{}, err
	}
	state, err := decodeExternalMaintenanceState(raw, found)
	if err != nil {
		return agentcompaction.Result{}, err
	}
	if receipt, ok := state.Receipts[commandID]; ok {
		return receipt, nil
	}
	controlState, err := control.read(ctx)
	if err != nil {
		return agentcompaction.Result{}, err
	}
	if control.active != nil || controlState.Phase != agentrun.RunPhaseIdle {
		return agentcompaction.Result{}, ErrOperationActive
	}
	result, err := maintain()
	if err != nil {
		return result, err
	}
	err = control.store.Update(context.WithoutCancel(ctx), capability, func(raw json.RawMessage, present bool) (json.RawMessage, error) {
		current, err := decodeExternalMaintenanceState(raw, present)
		if err != nil {
			return nil, err
		}
		current.record(commandID, result)
		return json.Marshal(current)
	})
	return result, err
}
