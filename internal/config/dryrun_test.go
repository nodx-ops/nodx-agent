package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The dry-run contract in one place: `--stdout` must boot on a machine with
// ZERO delivery configuration (no server, no secret, no claim token, no
// placement), because its whole purpose is first-run feedback for someone who
// has not deployed anything yet. It must still refuse a non-UUID identity,
// because collect.NewSample drops every sample with a non-UUID agent_id --
// which would make `--stdout` silently print nothing, the worst failure mode.
// dryRunConfig points NODX_AGENT_CONFIG at a file that EXISTS: an explicitly
// requested path that is missing is fatal by design (typo protection), so a
// dangling path would mask the very behaviour under test. An empty file
// parses to no values, leaving defaults + env -- deterministic, and it does
// not depend on whether /etc/nodx/agent.yaml happens to exist on the host.
func dryRunConfig(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "agent.yaml")
	if err := os.WriteFile(p, []byte("# test config: intentionally empty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadDryRunNeedsNoDeliveryConfig(t *testing.T) {
	t.Setenv("NODX_AGENT_CONFIG", dryRunConfig(t))
	// Unset (not set-to-""): overlayEnv uses os.LookupEnv, so a present-but
	// empty variable would overlay an empty string rather than leave the
	// default. t.Setenv first so the original value is restored at test end;
	// os.Unsetenv makes it genuinely absent for the duration.
	for _, k := range []string{"NODX_SERVER", "NODX_AGENT_SECRET", "AGENT_ID", "AGENT_AUTH_TOKEN", "DATACENTER_ID"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	c, err := LoadDryRun("")
	if err != nil {
		t.Fatalf("LoadDryRun with no delivery config must succeed, got: %v", err)
	}
	if c.AgentID != DemoAgentID {
		t.Fatalf("expected DemoAgentID default, got %q", c.AgentID)
	}
	if c.PollInterval != DefaultPollInterval {
		t.Fatalf("expected default poll interval, got %s", c.PollInterval)
	}
}

// A supplied AGENT_ID must always win over the demo default. Real identity
// is never overridden -- dry-run only fills an absence.
func TestLoadDryRunHonorsSuppliedAgentID(t *testing.T) {
	t.Setenv("NODX_AGENT_CONFIG", dryRunConfig(t))
	t.Setenv("AGENT_ID", testAgentID)

	c, err := LoadDryRun("")
	if err != nil {
		t.Fatalf("LoadDryRun: %v", err)
	}
	if c.AgentID != testAgentID {
		t.Fatalf("supplied AGENT_ID must win: want %q got %q", testAgentID, c.AgentID)
	}
	if c.AgentID == DemoAgentID {
		t.Fatal("demo default must not overwrite a supplied identity")
	}
}

// The failure mode that matters most: a malformed AGENT_ID reaching the
// collector would be rejected per-sample inside NewSample, producing zero
// output with no error. LoadDryRun must refuse it at boot instead.
func TestLoadDryRunRejectsNonUUIDAgentID(t *testing.T) {
	t.Setenv("NODX_AGENT_CONFIG", dryRunConfig(t))
	t.Setenv("AGENT_ID", "agent-nodx-primary")

	if _, err := LoadDryRun(""); err == nil {
		t.Fatal("expected error for non-UUID AGENT_ID, got nil")
	} else if !strings.Contains(err.Error(), "not a UUID") {
		t.Fatalf("expected UUID error, got: %v", err)
	}
}

// Dry-run validation must NOT re-impose delivery requirements -- that would
// defeat the purpose -- while still rejecting a nonsensical poll interval.
func TestValidateDryRunScope(t *testing.T) {
	base := Config{AgentID: DemoAgentID, PollInterval: 15 * time.Second}

	if err := base.ValidateDryRun(); err != nil {
		t.Fatalf("bare dry-run config must validate, got: %v", err)
	}

	// Delivery fields absent: still valid for dry-run.
	bare := Config{AgentID: DemoAgentID, PollInterval: time.Second}
	if err := bare.ValidateDryRun(); err != nil {
		t.Fatalf("dry-run must not require delivery fields, got: %v", err)
	}

	zeroPoll := Config{AgentID: DemoAgentID, PollInterval: 0}
	if err := zeroPoll.ValidateDryRun(); err == nil {
		t.Fatal("zero poll_interval must still be rejected")
	}

	noID := Config{PollInterval: time.Second}
	if err := noID.ValidateDryRun(); err == nil {
		t.Fatal("empty AgentID must be rejected (never reaches collector silently)")
	}
}

// Regression: Load (delivery path) keeps its full contract. Splitting
// loadConfig out of Load must not have loosened a single requirement.
func TestLoadStillEnforcesFullContract(t *testing.T) {
	t.Setenv("NODX_AGENT_CONFIG", dryRunConfig(t))
	// Unset (not set-to-""): overlayEnv uses os.LookupEnv, so a present-but
	// empty variable would overlay an empty string rather than leave the
	// default. t.Setenv first so the original value is restored at test end;
	// os.Unsetenv makes it genuinely absent for the duration.
	for _, k := range []string{"NODX_SERVER", "NODX_AGENT_SECRET", "AGENT_ID", "AGENT_AUTH_TOKEN", "DATACENTER_ID"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}

	if _, err := Load(""); err == nil {
		t.Fatal("Load must still refuse to boot without delivery config")
	} else if !strings.Contains(err.Error(), "NODX_SERVER") {
		t.Fatalf("expected NODX_SERVER error, got: %v", err)
	}
}
