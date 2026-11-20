package engine

import (
	"context"
	"sort"
)

// ModelRepo implementation for MemStore.

// ListModels implements ModelRepo.
func (m *MemStore) ListModels(_ context.Context) ([]*DeviceModel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*DeviceModel, 0, len(m.models))
	for _, mm := range m.models {
		out = append(out, cloneModel(mm))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModelID < out[j].ModelID })
	return out, nil
}

// ModelByID implements ModelRepo.
func (m *MemStore) ModelByID(_ context.Context, modelID string) (*DeviceModel, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	mm, ok := m.models[modelID]
	if !ok {
		return nil, ErrNotFound
	}
	return cloneModel(mm), nil
}

// SaveModel implements ModelRepo.
func (m *MemStore) SaveModel(_ context.Context, mm *DeviceModel) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.models[mm.ModelID] = cloneModel(mm)
	return nil
}

// DeleteModel implements ModelRepo. Like the Postgres store's ON DELETE SET NULL,
// the devices survive and simply stop being decoded.
func (m *MemStore) DeleteModel(_ context.Context, modelID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.models[modelID]; !ok {
		return ErrNotFound
	}
	delete(m.models, modelID)
	for _, d := range m.devices {
		if d.ModelID == modelID {
			d.ModelID = ""
		}
	}
	return nil
}

func cloneModel(in *DeviceModel) *DeviceModel {
	if in == nil {
		return nil
	}
	c := *in
	return &c
}
