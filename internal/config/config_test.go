package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testAgentID = "a1b2c3d4-1111-4111-8111-000000000001"
	testSecret  = "test-secret"
	testToken   = "test-claim-token"
	testDC      = "dc-test-1"
)

func validEnv(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("NODX_SERVER", "http://ingest.example.com:8080")
	t.Setenv("NODX_AGENT_SECRET", testSecret)
	t.Setenv("AGENT_ID", testAgentID)
	t.Setenv("AGENT_AUTH_TOKEN", testToken)
	t.Setenv("DATACENTER_ID", testDC)
	t.Setenv("NODX_AGENT_CONFIG", filepath.Join(dir, "missing.yaml"))
}

// Load must fail on the required identity/secret matrix: every missing or
// malformed value is a fatal operator error, never a default.
func TestValidate(t *testing.T) {
	base := Config{
		ServerURL: "http://ingest.example.com:8080", AgentSecret: testSecret,
		AgentID: testAgentID, AgentAuthToken: testToken, DatacenterID: testDC,
		StatePath: "/tmp/state.json", SpoolDir: "/tmp/spool", ObsAddr: "127.0.0.1:6060",
		PollInterval: 15 * time.Second, HeartbeatInterval: 60 * time.Second,
		RequestTimeout:  10 * time.Second,
		BatchMaxSamples: 100, BatchMaxBytes: 262144,
		SpoolMaxBytes: 50 << 20, SpoolMaxFiles: 10000,
	}
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"valid", func(c *Config) {}, ""},
		{"missing server", func(c *Config) { c.ServerURL = "" }, "NODX_SERVER"},
		{"bad scheme", func(c *Config) { c.ServerURL = "mqtt://x:1883" }, "not a valid http"},
		{"no host", func(c *Config) { c.ServerURL = "http://" }, "not a valid http"},
		{"missing secret", func(c *Config) { c.AgentSecret = "" }, "NODX_AGENT_SECRET"},
		{"missing agent id", func(c *Config) { c.AgentID = "" }, "AGENT_ID"},
		{"fabricated agent id", func(c *Config) { c.AgentID = "agent-1" }, "not a UUID"},
		{"missing claim token", func(c *Config) { c.AgentAuthToken = "" }, "AGENT_AUTH_TOKEN"},
		{"missing datacenter", func(c *Config) { c.DatacenterID = "" }, "DATACENTER_ID"},
		{"redfish stub enforced", func(c *Config) { c.RedfishEnabled = true }, "redfish"},
		{"zero poll", func(c *Config) { c.PollInterval = 0 }, "poll_interval"},
		{"zero heartbeat", func(c *Config) { c.HeartbeatInterval = 0 }, "heartbeat_interval"},
		{"zero timeout", func(c *Config) { c.RequestTimeout = 0 }, "request_timeout"},
		{"bad obs addr", func(c *Config) { c.ObsAddr = "nope" }, "obs_addr"},
		{"zero batch samples", func(c *Config) { c.BatchMaxSamples = 0 }, "batch_max_samples"},
		{"tiny batch bytes", func(c *Config) { c.BatchMaxBytes = 10 }, "batch_max_bytes"},
		{"zero spool files", func(c *Config) { c.SpoolMaxFiles = 0 }, "spool_max_files"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := base
			tc.mutate(&c)
			err := c.Validate()
			if tc.wantErr == "" && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tc.wantErr)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
				}
			}
		})
	}
}

// Env beats YAML beats defaults: every layer is exercised.
func TestLoadPrecedence(t *testing.T) {
	dir := t.TempDir()
	yamlPath := filepath.Join(dir, "agent.yaml")
	yaml := "server: http://yaml-host:8080\n" +
		"agent_secret: yaml-secret\n" +
		"agent_id: " + testAgentID + "\n" +
		"agent_auth_token: yaml-token\n" +
		"datacenter_id: dc-yaml\n" +
		"poll_interval: 30s\n" +
		"heartbeat_interval: 120s\n" +
		"# a comment line\n" +
		"obs_addr: \"127.0.0.1:6061\"\n"
	if err := os.WriteFile(yamlPath, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	// YAML-only: NODX_AGENT_CONFIG points at the file, no overlay env.
	t.Setenv("NODX_AGENT_CONFIG", yamlPath)
	t.Setenv("NODX_SERVER", "")
	t.Setenv("NODX_AGENT_SECRET", "")
	t.Setenv("AGENT_ID", "")
	t.Setenv("AGENT_AUTH_TOKEN", "")
	t.Setenv("DATACENTER_ID", "")
	os.Unsetenv("NODX_SERVER")
	os.Unsetenv("NODX_AGENT_SECRET")
	os.Unsetenv("AGENT_ID")
	os.Unsetenv("AGENT_AUTH_TOKEN")
	os.Unsetenv("DATACENTER_ID")

	c, err := Load("")
	if err != nil {
		t.Fatalf("yaml-only load: %v", err)
	}
	if c.ServerURL != "http://yaml-host:8080" {
		t.Fatalf("yaml server not applied: %q", c.ServerURL)
	}
	if c.PollInterval != 30*time.Second || c.HeartbeatInterval != 120*time.Second {
		t.Fatalf("yaml durations not applied: %s %s", c.PollInterval, c.HeartbeatInterval)
	}
	if c.ObsAddr != "127.0.0.1:6061" {
		t.Fatalf("quoted yaml value not applied: %q", c.ObsAddr)
	}
	if c.StatePath != DefaultStatePath || c.SpoolDir != DefaultSpoolDir {
		t.Fatalf("unset keys must fall back to defaults: %q %q", c.StatePath, c.SpoolDir)
	}

	// Env overlay wins over the file.
	t.Setenv("NODX_SERVER", "http://env-host:8080")
	t.Setenv("NODX_POLL_INTERVAL", "15s")
	c, err = Load("")
	if err != nil {
		t.Fatalf("env-overlay load: %v", err)
	}
	if c.ServerURL != "http://env-host:8080" {
		t.Fatalf("env did not win: %q", c.ServerURL)
	}
	if c.PollInterval != 15*time.Second {
		t.Fatalf("env duration did not win: %s", c.PollInterval)
	}
	if c.AgentSecret != "yaml-secret" {
		t.Fatalf("yaml value must survive where env is unset: %q", c.AgentSecret)
	}
}

// The default path may be absent (env-only containers); an explicit path may not.
func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	validEnv(t, dir)

	os.Unsetenv("NODX_AGENT_CONFIG")
	c, err := Load(filepath.Join(dir, "does-not-exist.yaml"))
	if err == nil {
		t.Fatal("explicit missing file must fail")
	}
	if c != (Config{}) {
		t.Fatal("failed load must return zero config")
	}
}

// Malformed YAML and unknown keys fail fast (typo protection).
func TestLoadBadYAML(t *testing.T) {
	dir := t.TempDir()
	validEnv(t, dir)
	bad := map[string]string{
		"no-colon.yaml":    "this line has no separator\n",
		"unknown-key.yaml": "frobnicate: true\n",
		"bad-dur.yaml":     "poll_interval: often\n",
	}
	for name, body := range bad {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Fatalf("%s: expected parse failure", name)
		}
	}
}

func TestIsUUID(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{testAgentID, true},
		{"A1B2C3D4-1111-4111-8111-000000000001", true},
		{"", false},
		{"agent-1", false},
		{"a1b2c3d4-1111-4111-8111-00000000001", false},  // short
		{"a1b2c3d4_1111_4111_8111_000000000001", false}, // wrong separators
		{"g1b2c3d4-1111-4111-8111-000000000001", false}, // non-hex
		{testAgentID + "x", false},                      // long
	}
	for _, tc := range cases {
		if got := IsUUID(tc.in); got != tc.want {
			t.Errorf("IsUUID(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
