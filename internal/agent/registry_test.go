package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadRegistry(t *testing.T) {
	content := `api_version: cova/v1
kind: AgentRegistry
workspace: test-project

agents:
  - agent_id: pm-001
    name: "PM Wang"
    role: PM
    agent_md: ../../agents/pm/agent.md
    enabled: true
  - agent_id: ba-001
    name: "BA Li"
    role: BA
    agent_md: ../../agents/ba/agent.md
    enabled: true
  - agent_id: arch-disabled
    name: "Arch Zhang"
    role: Arch
    enabled: false
`
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	reg, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("LoadRegistry: %v", err)
	}

	if reg.APIVersion != "cova/v1" {
		t.Errorf("api_version = %q, want cova/v1", reg.APIVersion)
	}
	if reg.Kind != "AgentRegistry" {
		t.Errorf("kind = %q, want AgentRegistry", reg.Kind)
	}
	if reg.Workspace != "test-project" {
		t.Errorf("workspace = %q, want test-project", reg.Workspace)
	}
	if len(reg.Agents) != 3 {
		t.Fatalf("len(agents) = %d, want 3", len(reg.Agents))
	}

	enabled := reg.EnabledAgents()
	if len(enabled) != 2 {
		t.Errorf("len(enabled) = %d, want 2", len(enabled))
	}

	pm := reg.Agents[0]
	if pm.AgentID != "pm-001" {
		t.Errorf("agents[0].agent_id = %q, want pm-001", pm.AgentID)
	}
	if pm.Name != "PM Wang" {
		t.Errorf("agents[0].name = %q, want PM Wang", pm.Name)
	}
	if pm.Role != "PM" {
		t.Errorf("agents[0].role = %q, want PM", pm.Role)
	}
	if !pm.Enabled {
		t.Error("agents[0].enabled should be true")
	}
}

func TestLoadRegistryWithAgents(t *testing.T) {
	content := `api_version: cova/v1
kind: AgentRegistry
workspace: dev

agents:
  - agent_id: pm-001
    name: "PM"
    role: PM
    enabled: true
  - agent_id: ba-001
    name: "BA"
    role: BA
    enabled: false
`
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.yaml")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	reg, agents, err := LoadRegistryWithAgents(path)
	if err != nil {
		t.Fatalf("LoadRegistryWithAgents: %v", err)
	}

	if reg.Workspace != "dev" {
		t.Errorf("workspace = %q, want dev", reg.Workspace)
	}
	// Only enabled agents are returned
	if len(agents) != 1 {
		t.Fatalf("len(agents) = %d, want 1 (only enabled)", len(agents))
	}
	if agents[0].ID != "pm-001" {
		t.Errorf("agents[0].ID = %q, want pm-001", agents[0].ID)
	}
}

func TestLoadRegistryValidation(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantErr bool
	}{
		{
			name: "missing api_version",
			content: `kind: AgentRegistry
agents:
  - agent_id: pm
    name: PM
    role: PM
    enabled: true`,
			wantErr: true,
		},
		{
			name: "missing kind",
			content: `api_version: cova/v1
agents:
  - agent_id: pm
    name: PM
    role: PM
    enabled: true`,
			wantErr: true,
		},
		{
			name: "duplicate agent_id",
			content: `api_version: cova/v1
kind: AgentRegistry
agents:
  - agent_id: pm
    name: PM1
    role: PM
    enabled: true
  - agent_id: pm
    name: PM2
    role: PM
    enabled: true`,
			wantErr: true,
		},
		{
			name: "missing name",
			content: `api_version: cova/v1
kind: AgentRegistry
agents:
  - agent_id: pm
    role: PM
    enabled: true`,
			wantErr: true,
		},
		{
			name: "missing role",
			content: `api_version: cova/v1
kind: AgentRegistry
agents:
  - agent_id: pm
    name: PM
    enabled: true`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "registry.yaml")
			if err := os.WriteFile(path, []byte(tt.content), 0644); err != nil {
				t.Fatalf("write: %v", err)
			}

			_, err := LoadRegistry(path)
			if tt.wantErr && err == nil {
				t.Error("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestLoadAgentMD(t *testing.T) {
	dir := t.TempDir()

	mdContent := "# Test Agent\nI am a test agent."
	mdPath := filepath.Join(dir, "agent.md")
	if err := os.WriteFile(mdPath, []byte(mdContent), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	registryPath := filepath.Join(dir, "registry.yaml")

	ref := RegistryRef{
		AgentID: "test",
		AgentMD: "agent.md", // relative to registry
	}

	content, err := LoadAgentMD(registryPath, ref)
	if err != nil {
		t.Fatalf("LoadAgentMD: %v", err)
	}
	if content != mdContent {
		t.Errorf("content = %q, want %q", content, mdContent)
	}

	// Empty agent_md returns empty string
	ref2 := RegistryRef{AgentID: "test2", AgentMD: ""}
	content2, err := LoadAgentMD(registryPath, ref2)
	if err != nil {
		t.Fatalf("LoadAgentMD empty: %v", err)
	}
	if content2 != "" {
		t.Errorf("expected empty, got %q", content2)
	}
}
