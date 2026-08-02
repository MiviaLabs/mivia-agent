package events

import "testing"

func TestNewIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		defName    string
		source     string
		instanceID string
		generation uint64
		wantErr    bool
	}{
		{"valid user", "agent1", "user", "inst-1", 1, false},
		{"valid workspace", "agent1", "workspace", "inst-1", 1, false},
		{"valid compiled", "agent1", "compiled", "inst-1", 1, false},
		{"empty name", "", "user", "inst-1", 1, true},
		{"empty source", "agent1", "", "inst-1", 1, true},
		{"invalid source", "agent1", "invalid", "inst-1", 1, true},
		{"empty instance", "agent1", "user", "", 1, true},
		{"zero generation", "agent1", "user", "inst-1", 0, true},
		{"name too long", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", "user", "inst-1", 1, true},
		{"instance too long", "a", "user", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1", 1, true},
		{"control char in name", "agent\x001", "user", "inst-1", 1, true},
		{"whitespace trimmed", "  agent1  ", "  user  ", "  inst-1  ", 1, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, err := NewIdentity(tt.defName, tt.source, tt.instanceID, tt.generation)
			if (err != nil) != tt.wantErr {
				t.Errorf("NewIdentity() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				if id.InstanceID != "inst-1" {
					t.Errorf("NewIdentity() InstanceID = %q, want inst-1", id.InstanceID)
				}
			}
		})
	}
}

func TestWithAgentAttribution(t *testing.T) {
	e := NewEvent(KindToolStart)
	e = e.WithAgentAttribution("task-1", "researcher", 2)
	if e.AgentTask != "task-1" || e.AgentName != "researcher" || e.AgentDepth != 2 {
		t.Errorf("WithAgentAttribution() = %+v", e)
	}
}

func TestNewEvent(t *testing.T) {
	e := NewEvent(KindToolEnd)
	if e.Kind != KindToolEnd {
		t.Errorf("NewEvent() Kind = %v, want %v", e.Kind, KindToolEnd)
	}
}
