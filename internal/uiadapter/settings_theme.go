package uiadapter

import (
	"context"
	"fmt"

	"github.com/MiviaLabs/mivia-agent/internal/uikit/ports"
)

// PersistTheme saves the selected TUI theme and waits for its terminal save
// event. The launcher uses this adapter method so it does not depend on the
// settings port vocabulary or know how save handles are implemented.
func (s *SettingsStore) PersistTheme(name string) error {
	handle, err := s.Settings().General.Apply(context.Background(), ports.ScopeUser, ports.SetTheme{Name: name})
	if err != nil {
		return err
	}
	for event := range handle.Events() {
		if event.State == ports.SaveFailed {
			return fmt.Errorf("%s", event.Message)
		}
	}
	return nil
}
