package registry

// Registry represents configs/experts/registry.yaml.
type Registry struct {
	APIVersion string        `yaml:"api_version"`
	Kind       string        `yaml:"kind"`
	Experts    []RegistryRef `yaml:"experts"`
}

// RegistryRef links an expert_id to a concrete expert config file.
type RegistryRef struct {
	ExpertID   string `yaml:"expert_id"`
	ConfigPath string `yaml:"config_path"`
	Enabled    bool   `yaml:"enabled"`
}

// EnabledExperts returns only enabled registry entries.
func (r Registry) EnabledExperts() []RegistryRef {
	out := make([]RegistryRef, 0, len(r.Experts))
	for _, expert := range r.Experts {
		if expert.Enabled {
			out = append(out, expert)
		}
	}
	return out
}

// ExpertDefinition represents experts/*/expert.yaml.
type ExpertDefinition struct {
	APIVersion string         `yaml:"api_version"`
	Kind       string         `yaml:"kind"`
	Metadata   ExpertMetadata `yaml:"metadata"`
	Spec       ExpertSpec     `yaml:"spec"`
}

type ExpertMetadata struct {
	ExpertID    string `yaml:"expert_id"`
	ExpertName  string `yaml:"expert_name"`
	ExpertType  string `yaml:"expert_type"`
	Version     string `yaml:"version"`
	Owner       string `yaml:"owner"`
	Description string `yaml:"description"`
}

type ExpertSpec struct {
	Enabled      bool          `yaml:"enabled"`
	Capabilities []string      `yaml:"capabilities"`
	Routing      ExpertRouting `yaml:"routing"`
	Runtime      ExpertRuntime `yaml:"runtime"`
	Inputs       ExpertInputs  `yaml:"inputs"`
	Outputs      ExpertOutputs `yaml:"outputs"`
}

type ExpertRouting struct {
	Priority int      `yaml:"priority"`
	Tags     []string `yaml:"tags"`
}

type ExpertRuntime struct {
	Mode           string `yaml:"mode"`
	Entrypoint     string `yaml:"entrypoint"`
	TimeoutSeconds int    `yaml:"timeout_seconds"`
	MaxConcurrency int    `yaml:"max_concurrency"`
}

type ExpertInputs struct {
	Accepts []string `yaml:"accepts"`
}

type ExpertOutputs struct {
	ResultSchema string `yaml:"result_schema"`
	EvidenceMode string `yaml:"evidence_mode"`
}

// LoadedExpert is a validated enabled expert in the active registry.
type LoadedExpert struct {
	Ref        RegistryRef
	ConfigPath string
	Definition ExpertDefinition
}

// Snapshot contains a validated registry and all enabled experts.
type Snapshot struct {
	Registry Registry
	Experts  []LoadedExpert
}
