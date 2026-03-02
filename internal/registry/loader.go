package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	apiVersionV1         = "cova/v1"
	registryKindV1       = "ExpertRegistry"
	expertDefinitionKind = "ExpertDefinition"
)

type validationErrors []string

func (v validationErrors) Error() string {
	return "validation failed: " + strings.Join(v, "; ")
}

func (v validationErrors) hasErrors() bool {
	return len(v) > 0
}

func (v *validationErrors) add(format string, args ...any) {
	*v = append(*v, fmt.Sprintf(format, args...))
}

// ResolveConfigPath resolves one expert config path against a registry file path.
func ResolveConfigPath(registryPath, configPath string) string {
	if filepath.IsAbs(configPath) {
		return filepath.Clean(configPath)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(registryPath), configPath))
}

// LoadRegistry loads and validates a registry file.
func LoadRegistry(registryPath string) (*Registry, error) {
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return nil, fmt.Errorf("read registry %q: %w", registryPath, err)
	}

	r, err := parseRegistry(raw)
	if err != nil {
		return nil, fmt.Errorf("decode registry %q: %w", registryPath, err)
	}

	if err := ValidateRegistry(&r); err != nil {
		return nil, err
	}
	return &r, nil
}

// ValidateRegistry validates base registry schema and semantics.
func ValidateRegistry(r *Registry) error {
	var errs validationErrors

	if strings.TrimSpace(r.APIVersion) != apiVersionV1 {
		errs.add("api_version must be %q", apiVersionV1)
	}
	if strings.TrimSpace(r.Kind) != registryKindV1 {
		errs.add("kind must be %q", registryKindV1)
	}
	if len(r.Experts) == 0 {
		errs.add("experts must contain at least one entry")
	}

	seen := make(map[string]struct{}, len(r.Experts))
	for idx, ref := range r.Experts {
		pathPrefix := fmt.Sprintf("experts[%d]", idx)
		expertID := strings.TrimSpace(ref.ExpertID)
		configPath := strings.TrimSpace(ref.ConfigPath)

		if expertID == "" {
			errs.add("%s.expert_id is required", pathPrefix)
		} else {
			if _, ok := seen[expertID]; ok {
				errs.add("%s.expert_id=%q is duplicated", pathPrefix, expertID)
			}
			seen[expertID] = struct{}{}
		}
		if configPath == "" {
			errs.add("%s.config_path is required", pathPrefix)
		}
	}

	if errs.hasErrors() {
		return errs
	}
	return nil
}

// LoadExpertDefinition loads and validates one expert definition file.
func LoadExpertDefinition(configPath string) (*ExpertDefinition, error) {
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("decode expert definition %q: %w", configPath, err)
	}

	def, err := parseExpertDefinition(raw)
	if err != nil {
		return nil, fmt.Errorf("decode expert definition %q: %w", configPath, err)
	}
	if err := ValidateExpertDefinition(&def); err != nil {
		return nil, err
	}
	return &def, nil
}

// ValidateExpertDefinition validates the minimum fields required by the registry stage.
func ValidateExpertDefinition(def *ExpertDefinition) error {
	var errs validationErrors
	allowedCapabilities := map[string]struct{}{
		"daily_brief": {},
		"query":       {},
	}

	if strings.TrimSpace(def.APIVersion) != apiVersionV1 {
		errs.add("api_version must be %q", apiVersionV1)
	}
	if strings.TrimSpace(def.Kind) != expertDefinitionKind {
		errs.add("kind must be %q", expertDefinitionKind)
	}
	if strings.TrimSpace(def.Metadata.ExpertID) == "" {
		errs.add("metadata.expert_id is required")
	}
	if strings.TrimSpace(def.Metadata.ExpertName) == "" {
		errs.add("metadata.expert_name is required")
	}
	if strings.TrimSpace(def.Metadata.ExpertType) == "" {
		errs.add("metadata.expert_type is required")
	}
	if strings.TrimSpace(def.Metadata.Version) == "" {
		errs.add("metadata.version is required")
	} else if !isSemver(strings.TrimSpace(def.Metadata.Version)) {
		errs.add("metadata.version must be semantic version like 1.2.3")
	}

	if len(def.Spec.Capabilities) == 0 {
		errs.add("spec.capabilities must contain at least one item")
	} else {
		for idx, capability := range def.Spec.Capabilities {
			capability = strings.TrimSpace(capability)
			if capability == "" {
				errs.add("spec.capabilities[%d] must not be empty", idx)
				continue
			}
			if _, ok := allowedCapabilities[capability]; !ok {
				errs.add("spec.capabilities[%d]=%q is unsupported", idx, capability)
			}
		}
	}
	if strings.TrimSpace(def.Spec.Runtime.Mode) == "" {
		errs.add("spec.runtime.mode is required")
	}
	if def.Spec.Runtime.TimeoutSeconds <= 0 {
		errs.add("spec.runtime.timeout_seconds must be > 0")
	}
	if def.Spec.Runtime.MaxConcurrency <= 0 {
		errs.add("spec.runtime.max_concurrency must be > 0")
	}
	if len(def.Spec.Inputs.Accepts) == 0 {
		errs.add("spec.inputs.accepts must contain at least one item")
	}
	if strings.TrimSpace(def.Spec.Outputs.ResultSchema) == "" {
		errs.add("spec.outputs.result_schema is required")
	}
	if strings.TrimSpace(def.Spec.Outputs.EvidenceMode) == "" {
		errs.add("spec.outputs.evidence_mode is required")
	} else if def.Spec.Outputs.EvidenceMode != "references" && def.Spec.Outputs.EvidenceMode != "citations" {
		errs.add("spec.outputs.evidence_mode must be \"references\" or \"citations\"")
	}

	if errs.hasErrors() {
		return errs
	}
	return nil
}

// LoadSnapshot loads registry + enabled expert definitions and checks expert_id alignment.
func LoadSnapshot(registryPath string) (*Snapshot, error) {
	registryPath = filepath.Clean(registryPath)
	r, err := LoadRegistry(registryPath)
	if err != nil {
		return nil, err
	}

	out := &Snapshot{
		Registry: *r,
		Experts:  make([]LoadedExpert, 0, len(r.EnabledExperts())),
	}

	for idx, ref := range r.Experts {
		if !ref.Enabled {
			continue
		}
		resolvedPath := ResolveConfigPath(registryPath, ref.ConfigPath)
		def, loadErr := LoadExpertDefinition(resolvedPath)
		if loadErr != nil {
			return nil, fmt.Errorf("experts[%d] expert_id=%q: %w", idx, ref.ExpertID, loadErr)
		}
		if def.Metadata.ExpertID != ref.ExpertID {
			return nil, fmt.Errorf(
				"experts[%d] expert_id=%q mismatch: metadata.expert_id=%q",
				idx,
				ref.ExpertID,
				def.Metadata.ExpertID,
			)
		}
		out.Experts = append(out.Experts, LoadedExpert{
			Ref:        ref,
			ConfigPath: resolvedPath,
			Definition: *def,
		})
	}

	return out, nil
}

func parseRegistry(raw []byte) (Registry, error) {
	lines := strings.Split(string(raw), "\n")
	var out Registry
	inExperts := false
	var current *RegistryRef

	for lineNo, line := range lines {
		trimmed := strings.TrimSpace(stripInlineComment(line))
		if trimmed == "" {
			continue
		}

		indent := leadingSpaces(line)
		if indent == 0 {
			inExperts = false
			if key, value, ok := splitYAMLKeyValue(trimmed); ok {
				switch key {
				case "api_version":
					out.APIVersion = value
				case "kind":
					out.Kind = value
				case "experts":
					if value != "" {
						return Registry{}, fmt.Errorf("line %d: experts must be a sequence", lineNo+1)
					}
					inExperts = true
				}
			}
			continue
		}

		if !inExperts {
			continue
		}

		if strings.HasPrefix(trimmed, "- ") {
			if current != nil {
				out.Experts = append(out.Experts, *current)
			}
			current = &RegistryRef{}
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if rest == "" {
				continue
			}
			if err := applyRegistryItemField(current, rest); err != nil {
				return Registry{}, fmt.Errorf("line %d: %w", lineNo+1, err)
			}
			continue
		}

		if current == nil {
			return Registry{}, fmt.Errorf("line %d: registry entry field found before list item", lineNo+1)
		}
		if err := applyRegistryItemField(current, trimmed); err != nil {
			return Registry{}, fmt.Errorf("line %d: %w", lineNo+1, err)
		}
	}

	if current != nil {
		out.Experts = append(out.Experts, *current)
	}
	return out, nil
}

func applyRegistryItemField(item *RegistryRef, line string) error {
	key, value, ok := splitYAMLKeyValue(line)
	if !ok {
		return fmt.Errorf("invalid field %q", line)
	}

	switch key {
	case "expert_id":
		item.ExpertID = value
	case "config_path":
		item.ConfigPath = value
	case "enabled":
		enabled, err := strconv.ParseBool(strings.ToLower(value))
		if err != nil {
			return fmt.Errorf("enabled must be true/false, got %q", value)
		}
		item.Enabled = enabled
	}
	return nil
}

func parseExpertDefinition(raw []byte) (ExpertDefinition, error) {
	lines := strings.Split(string(raw), "\n")
	var out ExpertDefinition

	type pathNode struct {
		indent int
		key    string
	}
	stack := make([]pathNode, 0, 6)

	for _, line := range lines {
		trimmed := strings.TrimSpace(stripInlineComment(line))
		if trimmed == "" {
			continue
		}
		indent := leadingSpaces(line)
		for len(stack) > 0 && indent <= stack[len(stack)-1].indent {
			stack = stack[:len(stack)-1]
		}

		if strings.HasPrefix(trimmed, "- ") {
			item := strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
			if item == "" {
				continue
			}
			pathParts := make([]string, 0, len(stack))
			for _, node := range stack {
				pathParts = append(pathParts, node.key)
			}
			parentPath := strings.Join(pathParts, ".")
			switch parentPath {
			case "spec.capabilities":
				out.Spec.Capabilities = append(out.Spec.Capabilities, unquoteYAMLScalar(item))
			case "spec.routing.tags":
				out.Spec.Routing.Tags = append(out.Spec.Routing.Tags, unquoteYAMLScalar(item))
			case "spec.inputs.accepts":
				out.Spec.Inputs.Accepts = append(out.Spec.Inputs.Accepts, unquoteYAMLScalar(item))
			}
			continue
		}

		key, value, ok := splitYAMLKeyValue(trimmed)
		if !ok {
			continue
		}

		if value == "" {
			stack = append(stack, pathNode{indent: indent, key: key})
			continue
		}

		pathParts := make([]string, 0, len(stack)+1)
		for _, node := range stack {
			pathParts = append(pathParts, node.key)
		}
		pathParts = append(pathParts, key)
		path := strings.Join(pathParts, ".")

		switch path {
		case "api_version":
			out.APIVersion = value
		case "kind":
			out.Kind = value
		case "metadata.expert_id":
			out.Metadata.ExpertID = value
		case "metadata.expert_name":
			out.Metadata.ExpertName = value
		case "metadata.expert_type":
			out.Metadata.ExpertType = value
		case "metadata.version":
			out.Metadata.Version = value
		case "metadata.owner":
			out.Metadata.Owner = value
		case "metadata.description":
			out.Metadata.Description = value
		case "spec.enabled":
			if b, err := strconv.ParseBool(strings.ToLower(value)); err == nil {
				out.Spec.Enabled = b
			}
		case "spec.routing.priority":
			if n, err := strconv.Atoi(value); err == nil {
				out.Spec.Routing.Priority = n
			}
		case "spec.runtime.mode":
			out.Spec.Runtime.Mode = value
		case "spec.runtime.entrypoint":
			out.Spec.Runtime.Entrypoint = value
		case "spec.runtime.timeout_seconds":
			if n, err := strconv.Atoi(value); err == nil {
				out.Spec.Runtime.TimeoutSeconds = n
			}
		case "spec.runtime.max_concurrency":
			if n, err := strconv.Atoi(value); err == nil {
				out.Spec.Runtime.MaxConcurrency = n
			}
		case "spec.outputs.result_schema":
			out.Spec.Outputs.ResultSchema = value
		case "spec.outputs.evidence_mode":
			out.Spec.Outputs.EvidenceMode = value
		}
	}
	return out, nil
}

func splitYAMLKeyValue(line string) (string, string, bool) {
	colon := strings.Index(line, ":")
	if colon <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(line[:colon])
	value := strings.TrimSpace(line[colon+1:])
	if key == "" {
		return "", "", false
	}
	return key, unquoteYAMLScalar(value), true
}

func unquoteYAMLScalar(value string) string {
	value = strings.TrimSpace(value)
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}

func stripInlineComment(line string) string {
	inSingle := false
	inDouble := false

	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return line[:i]
			}
		}
	}
	return line
}

func leadingSpaces(line string) int {
	count := 0
	for i := 0; i < len(line); i++ {
		if line[i] != ' ' {
			break
		}
		count++
	}
	return count
}

// IsValidationError reports whether the error was produced by field validation.
func IsValidationError(err error) bool {
	var vErr validationErrors
	return errors.As(err, &vErr)
}

func isSemver(v string) bool {
	semver := regexp.MustCompile(`^\d+\.\d+\.\d+$`)
	return semver.MatchString(v)
}
