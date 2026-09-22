package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
)

// nameRE：资源名合法性（MVP 约定：字母数字开头结尾，可含 . _ -，≤253 字符）。
var nameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9._-]{0,251}[A-Za-z0-9])?$`)

// ValidateName 校验资源名。名称在类型内唯一（§6.1）。
func ValidateName(name string) error {
	if !nameRE.MatchString(name) {
		return fmt.Errorf("%w: invalid name %q (must match %s)", ErrInvalid, name, nameRE.String())
	}
	return nil
}

// ValidateJSONObject 要求 raw 为 JSON 对象（或为空，视为 {}）。
// spec/status 以原始 JSON 透传存储，此处只约束形态，不解释字段。
func ValidateJSONObject(raw json.RawMessage, field string) error {
	if len(raw) == 0 {
		return nil
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Errorf("%w: %s must be a JSON object: %v", ErrInvalid, field, err)
	}
	return nil
}

// Validate  overall 校验 Machine（名称 + spec/status 形态 + 枚举）。
func (m *Machine) Validate() error {
	if err := ValidateName(m.Metadata.Name); err != nil {
		return err
	}
	if err := ValidateJSONObject(m.Spec, "spec"); err != nil {
		return err
	}
	if err := ValidateJSONObject(m.Status, "status"); err != nil {
		return err
	}
	var core MachineCoreSpec
	if len(m.Spec) != 0 {
		if err := json.Unmarshal(m.Spec, &core); err != nil {
			return fmt.Errorf("%w: spec: %v", ErrInvalid, err)
		}
	}
	switch core.ManagementMode {
	case "", ManagementModeAgentd, ManagementModeSSH:
		return nil
	default:
		return fmt.Errorf("%w: spec.managementMode must be %q or %q, got %q",
			ErrInvalid, ManagementModeAgentd, ManagementModeSSH, core.ManagementMode)
	}
}

// Validate 校验 AgentProfile（名称 + spec/status 形态）。
func (p *AgentProfile) Validate() error {
	if err := ValidateName(p.Metadata.Name); err != nil {
		return err
	}
	if err := ValidateJSONObject(p.Spec, "spec"); err != nil {
		return err
	}
	return ValidateJSONObject(p.Status, "status")
}
