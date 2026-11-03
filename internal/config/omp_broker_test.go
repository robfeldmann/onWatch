package config

import (
	"strings"
	"testing"
)

func TestLoadOMPBroker(t *testing.T) {
	t.Setenv("OMP_BROKER_URL", " http://127.0.0.1:8765/ ")
	t.Setenv("OMP_BROKER_TOKEN", "")
	t.Setenv("OMP_BROKER_TOKEN_COMMAND", " onwatch-token omp-broker ")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.OMPBrokerURL != "http://127.0.0.1:8765" {
		t.Errorf("OMPBrokerURL = %q", cfg.OMPBrokerURL)
	}
	if cfg.OMPBrokerTokenCommand != "onwatch-token omp-broker" {
		t.Errorf("OMPBrokerTokenCommand = %q", cfg.OMPBrokerTokenCommand)
	}
	if !cfg.HasProvider("omp") {
		t.Error("omp provider not enabled by OMP_BROKER_URL")
	}
	found := false
	for _, p := range cfg.AvailableProviders() {
		found = found || p == "omp"
	}
	if !found {
		t.Errorf("AvailableProviders() = %v, want omp", cfg.AvailableProviders())
	}
	if s := cfg.String(); strings.Contains(s, "onwatch-token") {
		t.Errorf("String() leaks the token command: %s", s)
	}
}

func TestLoadOMPBrokerDisabledWithoutURL(t *testing.T) {
	t.Setenv("OMP_BROKER_URL", "")
	t.Setenv("OMP_BROKER_TOKEN", "secret")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.HasProvider("omp") {
		t.Error("omp provider enabled without OMP_BROKER_URL")
	}
}
