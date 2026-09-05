package config_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/monshunter/xgoal/internal/config"
)

const configuredServices = `services:
  - id: app
    argv: [python3, app.py]
    dependsOn: [db]
    readiness: {argv: [sh, ready.sh], timeout: 10s, interval: 50ms}
    stopGracePeriod: 1s
  - id: unused
    argv: [sleep, '60']
    readiness: {argv: [true], timeout: 1s, interval: 50ms}
    stopGracePeriod: 1s
  - id: db
    argv: [python3, db.py]
    readiness: {argv: [sh, db-ready.sh], timeout: 10s, interval: 50ms}
    stopGracePeriod: 1s
`

func TestConfiguredServicesAreAccepted(t *testing.T) {
	if _, err := config.Load(strings.NewReader(validConfig + configuredServices)); err != nil {
		t.Fatal(err)
	}
}

func TestServiceSelectionUsesOnlyStableDependencyClosure(t *testing.T) {
	cfg, err := config.Load(strings.NewReader(validConfig + configuredServices))
	if err != nil {
		t.Fatal(err)
	}
	services, err := cfg.ServiceOrder([]string{"app"})
	if err != nil || len(services) != 2 || services[0].ID != "db" || services[1].ID != "app" {
		t.Fatalf("dependency closure: %+v %v", services, err)
	}
	services, err = cfg.ServiceOrder(nil)
	if err != nil || len(services) != 0 {
		t.Fatalf("empty request started services: %+v %v", services, err)
	}
	for name, input := range map[string]string{
		"missing":      strings.Replace(configuredServices, "dependsOn: [db]", "dependsOn: [missing]", 1),
		"cycle":        strings.Replace(configuredServices, "  - id: db", "  - id: db\n    dependsOn: [app]", 1),
		"duplicate":    strings.Replace(configuredServices, "id: unused", "id: app", 1),
		"escaping-id":  strings.Replace(configuredServices, "id: app", "id: ../app", 1),
		"escaping-cwd": strings.Replace(configuredServices, "id: app", "id: app\n    cwd: ../outside", 1),
		"unbounded":    strings.Replace(configuredServices, "timeout: 10s", "timeout: 0s", 1),
		"interval":     strings.Replace(configuredServices, "interval: 50ms", "interval: 20s", 1),
		"stop":         strings.Replace(configuredServices, "stopGracePeriod: 1s", "stopGracePeriod: 0s", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := config.Load(strings.NewReader(validConfig + input)); err == nil {
				t.Fatal("invalid service configuration accepted")
			}
		})
	}
}

func TestAbsentBootstrapEnvironmentPreservesLegacyIdentity(t *testing.T) {
	cfg, err := config.Load(strings.NewReader(validConfig + "bootstrap: {commands: [{id: prepare, argv: [true], timeout: 1s}]}\n"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(cfg.Bootstrap.Commands[0])
	if err != nil || strings.Contains(string(raw), `"env"`) || strings.Contains(string(raw), `"trustedFiles"`) {
		t.Fatalf("absent fields changed legacy command identity: %s %v", raw, err)
	}
}
