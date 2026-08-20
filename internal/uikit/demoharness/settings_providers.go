package demoharness

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// harnessProviders is the ports.ProviderSettings adapter.
type harnessProviders struct{ *Harness }

func (p harnessProviders) Providers() []ports.ProviderView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]ports.ProviderView, len(p.settingsProviders))
	copy(out, p.settingsProviders)
	return out
}

func (p harnessProviders) Apply(_ context.Context, _ ports.Scope, e ports.ProviderEdit) (ports.SaveHandle, error) {
	return p.newSaveHandle(func() error { return p.applyProvider(e) }), nil
}

func (h *Harness) findProvider(name string) int {
	for i := range h.settingsProviders {
		if h.settingsProviders[i].Name == name {
			return i
		}
	}
	return -1
}

func (h *Harness) applyProvider(e ports.ProviderEdit) error {
	switch v := e.(type) {
	case ports.UpsertProvider:
		if i := h.findProvider(v.Provider.Name); i >= 0 {
			h.settingsProviders[i] = v.Provider
			return nil
		}
		h.settingsProviders = append(h.settingsProviders, v.Provider)
	case ports.RemoveProvider:
		i := h.findProvider(v.Name)
		if i < 0 {
			return fmt.Errorf("provider %q not found", v.Name)
		}
		h.settingsProviders = append(h.settingsProviders[:i], h.settingsProviders[i+1:]...)
	case ports.UpsertModel:
		i := h.findProvider(v.Provider)
		if i < 0 {
			return fmt.Errorf("provider %q not found", v.Provider)
		}
		return h.upsertModel(i, v.Model)
	case ports.RemoveModel:
		i := h.findProvider(v.Provider)
		if i < 0 {
			return fmt.Errorf("provider %q not found", v.Provider)
		}
		return h.removeModel(i, v.Model)
	case ports.ActivateModel:
		return h.activateModel(v.Provider, v.Model)
	default:
		return fmt.Errorf("unknown provider edit %T", e)
	}
	return nil
}

func (h *Harness) upsertModel(providerIdx int, m ports.ModelView) error {
	models := h.settingsProviders[providerIdx].Models
	for i := range models {
		if models[i].Name == m.Name {
			models[i] = m
			return nil
		}
	}
	h.settingsProviders[providerIdx].Models = append(models, m)
	return nil
}

func (h *Harness) removeModel(providerIdx int, name string) error {
	models := h.settingsProviders[providerIdx].Models
	for i := range models {
		if models[i].Name == name {
			h.settingsProviders[providerIdx].Models = append(models[:i], models[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("model %q not found", name)
}

// activateModel marks provider/model as the active selection. Every
// other provider's Active flips false, matching config.ProviderModelGroup's
// single-active-provider invariant.
func (h *Harness) activateModel(provider, model string) error {
	i := h.findProvider(provider)
	if i < 0 {
		return fmt.Errorf("provider %q not found", provider)
	}
	found := false
	for _, m := range h.settingsProviders[i].Models {
		if m.Name == model {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("model %q not found under provider %q", model, provider)
	}
	for j := range h.settingsProviders {
		h.settingsProviders[j].Active = j == i
	}
	h.settingsProviders[i].ActiveModel = model
	return nil
}
