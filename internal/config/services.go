package config

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// Service runs business source from the candidate Tree. Only explicitly named
// control dependencies and readiness commands belong to the trusted baseline.
type Service struct {
	ID              string      `yaml:"id" json:"id"`
	Argv            []string    `yaml:"argv" json:"argv"`
	CWD             string      `yaml:"cwd,omitempty" json:"cwd,omitempty"`
	DependsOn       []string    `yaml:"dependsOn,omitempty" json:"dependsOn,omitempty"`
	TrustedFiles    []string    `yaml:"trustedFiles,omitempty" json:"trustedFiles,omitempty"`
	Environment     Environment `yaml:"env,omitempty" json:"env,omitempty"`
	Network         string      `yaml:"network,omitempty" json:"network,omitempty"`
	Readiness       Readiness   `yaml:"readiness" json:"readiness"`
	StopGracePeriod Duration    `yaml:"stopGracePeriod" json:"stopGracePeriod"`
}

type Readiness struct {
	Argv         []string `yaml:"argv" json:"argv"`
	TrustedFiles []string `yaml:"trustedFiles,omitempty" json:"trustedFiles,omitempty"`
	Timeout      Duration `yaml:"timeout" json:"timeout"`
	Interval     Duration `yaml:"interval" json:"interval"`
}

func (c Config) validateServices() error {
	ids := make([]string, 0, len(c.Services))
	for i, service := range c.Services {
		prefix := fmt.Sprintf("services[%d]", i)
		if !validComponentID(service.ID) {
			return fmt.Errorf("%s.id must be a safe identifier", prefix)
		}
		ids = append(ids, service.ID)
		if err := validateCommandArguments(prefix+".argv", service.Argv); err != nil {
			return err
		}
		if !validRelativePath(service.CWD) {
			return fmt.Errorf("%s.cwd must be repository-relative", prefix)
		}
		if service.StopGracePeriod.Duration <= 0 {
			return fmt.Errorf("%s.stopGracePeriod must be positive", prefix)
		}
		if service.Network != "" && !oneOf(service.Network, "deny", "allow", "require-gate") {
			return fmt.Errorf("%s.network is unsupported", prefix)
		}
		if err := validateNames(prefix+".dependsOn", service.DependsOn, validComponentID); err != nil {
			return err
		}
		if err := validateNames(prefix+".env.allow", service.Environment.Allow, validEnvironmentName); err != nil {
			return err
		}
		if err := validateNames(prefix+".trustedFiles", service.TrustedFiles, validTrustedPath); err != nil {
			return err
		}
		if err := validateCommandArguments(prefix+".readiness.argv", service.Readiness.Argv); err != nil {
			return err
		}
		if service.Readiness.Timeout.Duration <= 0 || service.Readiness.Interval.Duration <= 0 || service.Readiness.Interval.Duration > service.Readiness.Timeout.Duration {
			return fmt.Errorf("%s.readiness timeout and interval must be positive, interval <= timeout", prefix)
		}
		if err := validateNames(prefix+".readiness.trustedFiles", service.Readiness.TrustedFiles, validTrustedPath); err != nil {
			return err
		}
	}
	if _, err := c.ServiceOrder(ids); err != nil {
		return err
	}
	for _, validator := range c.Validators {
		if err := validateNames("validator "+validator.ID+" services", validator.Services, validComponentID); err != nil {
			return err
		}
		if _, err := c.ServiceOrder(validator.Services); err != nil {
			return fmt.Errorf("validator %q services: %w", validator.ID, err)
		}
	}
	return nil
}

// ServiceOrder returns only the selected dependency closure in stable startup
// order. An empty request starts no services; it never means all services.
func (c Config) ServiceOrder(requested []string) ([]Service, error) {
	byID := make(map[string]Service, len(c.Services))
	for _, service := range c.Services {
		if _, exists := byID[service.ID]; exists {
			return nil, fmt.Errorf("duplicate service %q", service.ID)
		}
		byID[service.ID] = service
	}
	state := map[string]int{}
	result := []Service{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 2 {
			return nil
		}
		if state[id] == 1 {
			return fmt.Errorf("service dependency cycle at %q", id)
		}
		service, exists := byID[id]
		if !exists {
			return fmt.Errorf("unknown service dependency %q", id)
		}
		state[id] = 1
		dependencies := append([]string(nil), service.DependsOn...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		state[id] = 2
		result = append(result, service)
		return nil
	}
	ids := append([]string(nil), requested...)
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func validComponentID(value string) bool {
	if value == "" || value == "." || value == ".." {
		return false
	}
	for _, c := range value {
		if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", c) {
			return false
		}
	}
	return true
}

func validTrustedPath(value string) bool {
	return value != "" && value != "." && validRelativePath(value) && !strings.ContainsAny(value, "*?[]\r\n")
}

func validateCommandArguments(field string, argv []string) error {
	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return fmt.Errorf("%s is required", field)
	}
	for _, arg := range argv {
		if arg == "" || !utf8.ValidString(arg) || strings.ContainsRune(arg, '\x00') {
			return errors.New(field + " contains an invalid argument")
		}
	}
	return nil
}
