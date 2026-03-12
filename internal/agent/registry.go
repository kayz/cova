package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	apiVersionV1   = "cova/v1"
	registryKindV1 = "AgentRegistry"
)

// Registry represents configs/agents/registry.yaml.
type Registry struct {
	APIVersion string         `yaml:"api_version"`
	Kind       string         `yaml:"kind"`
	Workspace  string         `yaml:"workspace"`
	Agents     []RegistryRef  `yaml:"agents"`
}

// RegistryRef links an agent_id to its configuration.
type RegistryRef struct {
	AgentID    string `yaml:"agent_id"`
	Name       string `yaml:"name"`
	Role       string `yaml:"role"`
	AgentMD    string `yaml:"agent_md"`
	Enabled    bool   `yaml:"enabled"`
}

// EnabledAgents returns only enabled agents.
func (r Registry) EnabledAgents() []RegistryRef {
	out := make([]RegistryRef, 0, len(r.Agents))
	for _, a := range r.Agents {
		if a.Enabled {
			out = append(out, a)
		}
	}
	return out
}

// LoadRegistry loads and validates an agent registry file.
func LoadRegistry(registryPath string) (*Registry, error) {
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return nil, fmt.Errorf("read agent registry %q: %w", registryPath, err)
	}

	r, err := parseAgentRegistry(raw)
	if err != nil {
		return nil, fmt.Errorf("parse agent registry %q: %w", registryPath, err)
	}

	if err := validateRegistry(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// LoadAgentMD reads the agent.md file for a given agent.
func LoadAgentMD(registryPath string, ref RegistryRef) (string, error) {
	if ref.AgentMD == "" {
		return "", nil
	}
	mdPath := ref.AgentMD
	if !filepath.IsAbs(mdPath) {
		mdPath = filepath.Clean(filepath.Join(filepath.Dir(registryPath), mdPath))
	}
	raw, err := os.ReadFile(mdPath)
	if err != nil {
		return "", fmt.Errorf("read agent.md for %q at %q: %w", ref.AgentID, mdPath, err)
	}
	return string(raw), nil
}

// LoadRegistryWithAgents loads the registry and all enabled agent info.
func LoadRegistryWithAgents(registryPath string) (*Registry, []Info, error) {
	r, err := LoadRegistry(registryPath)
	if err != nil {
		return nil, nil, err
	}

	agents := make([]Info, 0, len(r.Agents))
	for _, ref := range r.EnabledAgents() {
		agents = append(agents, Info{
			ID:          ref.AgentID,
			Name:        ref.Name,
			Role:        ref.Role,
			AgentMDPath: ref.AgentMD,
			Enabled:     ref.Enabled,
		})
	}
	return r, agents, nil
}

func validateRegistry(r *Registry) error {
	var errs []string

	if strings.TrimSpace(r.APIVersion) != apiVersionV1 {
		errs = append(errs, fmt.Sprintf("api_version must be %q", apiVersionV1))
	}
	if strings.TrimSpace(r.Kind) != registryKindV1 {
		errs = append(errs, fmt.Sprintf("kind must be %q", registryKindV1))
	}

	seen := make(map[string]struct{}, len(r.Agents))
	for idx, ref := range r.Agents {
		prefix := fmt.Sprintf("agents[%d]", idx)
		id := strings.TrimSpace(ref.AgentID)
		if id == "" {
			errs = append(errs, fmt.Sprintf("%s.agent_id is required", prefix))
		} else if _, ok := seen[id]; ok {
			errs = append(errs, fmt.Sprintf("%s.agent_id=%q is duplicated", prefix, id))
		} else {
			seen[id] = struct{}{}
		}
		if strings.TrimSpace(ref.Name) == "" {
			errs = append(errs, fmt.Sprintf("%s.name is required", prefix))
		}
		if strings.TrimSpace(ref.Role) == "" {
			errs = append(errs, fmt.Sprintf("%s.role is required", prefix))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("agent registry validation failed: %s", strings.Join(errs, "; "))
	}
	return nil
}

func parseAgentRegistry(raw []byte) (Registry, error) {
	lines := strings.Split(string(raw), "\n")
	var out Registry
	inAgents := false
	var current *RegistryRef

	for lineNo, line := range lines {
		trimmed := strings.TrimSpace(stripComment(line))
		if trimmed == "" {
			continue
		}

		indent := countLeadingSpaces(line)
		if indent == 0 {
			inAgents = false
			if key, value, ok := splitKV(trimmed); ok {
				switch key {
				case "api_version":
					out.APIVersion = value
				case "kind":
					out.Kind = value
				case "workspace":
					out.Workspace = value
				case "agents":
					if value != "" {
						return Registry{}, fmt.Errorf("line %d: agents must be a sequence", lineNo+1)
					}
					inAgents = true
				}
			}
			continue
		}

		if !inAgents {
			continue
		}

		if strings.HasPrefix(trimmed, "- ") {
			if current != nil {
				out.Agents = append(out.Agents, *current)
			}
			current = &RegistryRef{}
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if rest == "" {
				continue
			}
			applyAgentField(current, rest)
			continue
		}

		if current == nil {
			return Registry{}, fmt.Errorf("line %d: field found before list item", lineNo+1)
		}
		applyAgentField(current, trimmed)
	}

	if current != nil {
		out.Agents = append(out.Agents, *current)
	}
	return out, nil
}

func applyAgentField(item *RegistryRef, line string) {
	key, value, ok := splitKV(line)
	if !ok {
		return
	}
	switch key {
	case "agent_id":
		item.AgentID = value
	case "name":
		item.Name = value
	case "role":
		item.Role = value
	case "agent_md":
		item.AgentMD = value
	case "enabled":
		item.Enabled = strings.EqualFold(value, "true")
	}
}

func splitKV(line string) (string, string, bool) {
	colon := strings.Index(line, ":")
	if colon <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:colon])
	value := strings.TrimSpace(line[colon+1:])
	if key == "" {
		return "", "", false
	}
	return key, unquote(value), true
}

func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func stripComment(line string) string {
	inQ := false
	for i := 0; i < len(line); i++ {
		if line[i] == '"' || line[i] == '\'' {
			inQ = !inQ
		}
		if line[i] == '#' && !inQ {
			return line[:i]
		}
	}
	return line
}

func countLeadingSpaces(line string) int {
	n := 0
	for i := 0; i < len(line) && line[i] == ' '; i++ {
		n++
	}
	return n
}
