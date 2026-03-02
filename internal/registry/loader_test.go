package registry

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSnapshotSuccess(t *testing.T) {
	root := t.TempDir()

	registryPath := filepath.Join(root, "configs", "experts", "registry.yaml")
	writeFile(t, registryPath, `
api_version: cova/v1
kind: ExpertRegistry
experts:
  - expert_id: fi_cn_primary
    config_path: ../../experts/fi/expert.yaml
    enabled: true
  - expert_id: macro_cn
    config_path: ../../experts/macro/expert.yaml
    enabled: false
`)

	writeFile(t, filepath.Join(root, "experts", "fi", "expert.yaml"), `
api_version: cova/v1
kind: ExpertDefinition
metadata:
  expert_id: fi_cn_primary
  expert_name: FICC Observor CN Primary
  expert_type: fixed_income
  version: 1.0.0
spec:
  capabilities:
    - daily_brief
    - query
  runtime:
    mode: local
    entrypoint: ./main
    timeout_seconds: 300
    max_concurrency: 4
  inputs:
    accepts:
      - text
  outputs:
    result_schema: ./schemas/result.schema.json
    evidence_mode: references
`)

	// Disabled entries are not loaded in snapshot.
	snapshot, err := LoadSnapshot(registryPath)
	if err != nil {
		t.Fatalf("LoadSnapshot failed: %v", err)
	}
	if len(snapshot.Experts) != 1 {
		t.Fatalf("expected 1 enabled expert, got %d", len(snapshot.Experts))
	}
	if snapshot.Experts[0].Ref.ExpertID != "fi_cn_primary" {
		t.Fatalf("unexpected expert id: %s", snapshot.Experts[0].Ref.ExpertID)
	}
}

func TestLoadRegistryDuplicateExpertID(t *testing.T) {
	registryPath := filepath.Join(t.TempDir(), "registry.yaml")
	writeFile(t, registryPath, `
api_version: cova/v1
kind: ExpertRegistry
experts:
  - expert_id: fi_cn_primary
    config_path: ./a.yaml
    enabled: true
  - expert_id: fi_cn_primary
    config_path: ./b.yaml
    enabled: true
`)

	_, err := LoadRegistry(registryPath)
	if err == nil {
		t.Fatal("expected duplicate expert_id validation error")
	}
	if !strings.Contains(err.Error(), "duplicated") {
		t.Fatalf("expected duplicate error message, got: %v", err)
	}
}

func TestLoadSnapshotExpertIDMismatch(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "configs", "experts", "registry.yaml")
	writeFile(t, registryPath, `
api_version: cova/v1
kind: ExpertRegistry
experts:
  - expert_id: fi_cn_primary
    config_path: ../../experts/fi/expert.yaml
    enabled: true
`)

	writeFile(t, filepath.Join(root, "experts", "fi", "expert.yaml"), `
api_version: cova/v1
kind: ExpertDefinition
metadata:
  expert_id: fi_cn_secondary
  expert_name: mismatch
  expert_type: fixed_income
  version: 1.0.0
spec:
  capabilities:
    - daily_brief
  runtime:
    mode: local
    entrypoint: ./main
    timeout_seconds: 300
    max_concurrency: 1
  inputs:
    accepts:
      - text
  outputs:
    result_schema: ./schemas/result.schema.json
    evidence_mode: references
`)

	_, err := LoadSnapshot(registryPath)
	if err == nil {
		t.Fatal("expected expert_id mismatch error")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("expected mismatch error, got: %v", err)
	}
}

func TestLoadSnapshotMissingConfig(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "configs", "experts", "registry.yaml")
	writeFile(t, registryPath, `
api_version: cova/v1
kind: ExpertRegistry
experts:
  - expert_id: fi_cn_primary
    config_path: ../../experts/fi/expert.yaml
    enabled: true
`)

	_, err := LoadSnapshot(registryPath)
	if err == nil {
		t.Fatal("expected missing expert file error")
	}
	if !strings.Contains(err.Error(), "decode expert definition") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadSnapshotRejectsInvalidConformance(t *testing.T) {
	root := t.TempDir()
	registryPath := filepath.Join(root, "configs", "experts", "registry.yaml")
	writeFile(t, registryPath, `
api_version: cova/v1
kind: ExpertRegistry
experts:
  - expert_id: fi_cn_primary
    config_path: ../../experts/fi/expert.yaml
    enabled: true
`)

	writeFile(t, filepath.Join(root, "experts", "fi", "expert.yaml"), `
api_version: cova/v1
kind: ExpertDefinition
metadata:
  expert_id: fi_cn_primary
  expert_name: FICC Observor CN Primary
  expert_type: fixed_income
  version: 1.0
spec:
  capabilities:
    - unsupported_thing
  runtime:
    mode: local
    entrypoint: ./main
    timeout_seconds: 0
    max_concurrency: 0
  inputs:
    accepts: []
  outputs:
    evidence_mode: references
`)

	_, err := LoadSnapshot(registryPath)
	if err == nil {
		t.Fatal("expected conformance validation error")
	}
	if !strings.Contains(err.Error(), "metadata.version") && !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) failed: %v", path, err)
	}
	trimmed := strings.TrimLeft(content, "\n")
	if err := os.WriteFile(path, []byte(trimmed), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) failed: %v", path, err)
	}
}
