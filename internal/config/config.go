// Package config loads agent configuration with env > YAML > defaults
// precedence and fail-fast validation. Secrets (NODX_AGENT_SECRET,
// AGENT_AUTH_TOKEN) have no defaults: a missing or malformed value is a
// fatal operator error, never a silent fallback.
package config

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	DefaultConfigPath = "/etc/nodx/agent.yaml"
	DefaultStatePath  = "/var/lib/nodx-agent/state.json"
	DefaultSpoolDir   = "/var/lib/nodx-agent/spool"
	DefaultObsAddr    = "127.0.0.1:6060"

	DefaultPollInterval      = 15 * time.Second
	DefaultHeartbeatInterval = 60 * time.Second
	DefaultRequestTimeout    = 10 * time.Second

	DefaultBatchMaxSamples = 100
	DefaultBatchMaxBytes   = 256 * 1024 // 256KB; ingestion imposes no size limit, this is a self-cap

	DefaultSpoolMaxBytes = 50 * 1024 * 1024 // 50MB
	DefaultSpoolMaxFiles = 10000

	// DemoAgentID is the identity used by `--stdout` when AGENT_ID is unset.
	// It is deliberately a fixed, obviously-synthetic UUID (version-5 shape,
	// all-zero namespace) so that demo output is reproducible and can never be
	// mistaken for, or collide with, a real claimed agent. It is used ONLY for
	// local dry-run display; nothing is registered, stored, or transmitted.
	// Setting AGENT_ID overrides it -- real identity always wins.
	DemoAgentID = "00000000-0000-5000-8000-000000000000"
)

// Config is the full runtime configuration of the agent.
type Config struct {
	ServerURL      string // NODX_SERVER, e.g. http://ingest.example.com:8080
	AgentSecret    string // NODX_AGENT_SECRET, Bearer for POST /api/v1/telemetry
	AgentID        string // AGENT_ID, must be agents.id UUID (human-claimed, verified only)
	AgentAuthToken string // AGENT_AUTH_TOKEN, pairing claim token (stored, verify-only)
	DatacenterID   string // DATACENTER_ID

	ConfigPath string // resolved YAML path ("" when env-only)
	StatePath  string
	SpoolDir   string
	ObsAddr    string // loopback observability listener, host:port

	PollInterval      time.Duration
	HeartbeatInterval time.Duration
	RequestTimeout    time.Duration

	RedfishEnabled bool // stub flag only: on-agent Redfish is DEFERRED, must stay false (R26)

	BatchMaxSamples int
	BatchMaxBytes   int
	SpoolMaxBytes   int64
	SpoolMaxFiles   int
}

func defaults() Config {
	return Config{
		ConfigPath:        DefaultConfigPath,
		StatePath:         DefaultStatePath,
		SpoolDir:          DefaultSpoolDir,
		ObsAddr:           DefaultObsAddr,
		PollInterval:      DefaultPollInterval,
		HeartbeatInterval: DefaultHeartbeatInterval,
		RequestTimeout:    DefaultRequestTimeout,
		RedfishEnabled:    false,
		BatchMaxSamples:   DefaultBatchMaxSamples,
		BatchMaxBytes:     DefaultBatchMaxBytes,
		SpoolMaxBytes:     DefaultSpoolMaxBytes,
		SpoolMaxFiles:     DefaultSpoolMaxFiles,
	}
}

// loadConfig resolves the YAML path (flag > NODX_AGENT_CONFIG > default),
// parses it when present, and overlays environment. Validation is the
// caller's job: delivery (--normal) and dry-run (--stdout) demand genuinely
// different contracts from an identical resolution, and folding validation
// back in here would force one mode's requirements onto the other.
// A missing default file is OK (env-only mode); a missing explicitly
// requested file is fatal.
func loadConfig(argPath string) (Config, error) {
	c := defaults()

	path, explicit := resolvePath(argPath)
	raw, err := os.ReadFile(path)
	if err != nil {
		if explicit || !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("config: read %s: %w", path, err)
		}
		// env-only mode: default file absent, keep defaults, record intent.
		c.ConfigPath = ""
	} else {
		values, err := parseFlatYAML(string(raw))
		if err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		if err := applyMap(&c, values); err != nil {
			return Config{}, fmt.Errorf("config: parse %s: %w", path, err)
		}
		c.ConfigPath = path
	}

	if err := overlayEnv(&c); err != nil {
		return Config{}, fmt.Errorf("config: env: %w", err)
	}
	return c, nil
}

// Load resolves configuration and enforces the full delivery contract: a
// reachable endpoint, non-defaultable credentials, claimed identity, and
// usable timing/paths. Any validation failure is fatal.
func Load(argPath string) (Config, error) {
	c, err := loadConfig(argPath)
	if err != nil {
		return Config{}, err
	}
	if err := c.Validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// LoadDryRun resolves configuration for `--stdout` and enforces only what
// collection needs: a usable agent identity and a positive poll interval.
// NODX_SERVER / NODX_AGENT_SECRET / AGENT_AUTH_TOKEN / DATACENTER_ID are
// deliberately NOT required -- a dry run opens no socket, writes no state
// file, and creates no spool. When AGENT_ID is absent, DemoAgentID is used;
// a supplied AGENT_ID always wins, so real identity is never overridden.
func LoadDryRun(argPath string) (Config, error) {
	c, err := loadConfig(argPath)
	if err != nil {
		return Config{}, err
	}
	if c.AgentID == "" {
		c.AgentID = DemoAgentID
	}
	if err := c.ValidateDryRun(); err != nil {
		return Config{}, err
	}
	return c, nil
}

func resolvePath(argPath string) (string, bool) {
	if argPath != "" {
		return argPath, true
	}
	if p, ok := os.LookupEnv("NODX_AGENT_CONFIG"); ok && p != "" {
		return p, true
	}
	// R41: USB-stick zero-touch fallback — if default YAML missing but
	// /media/usb/nodx-config.yaml exists (FAT32 stick, janitor-plugged),
	// copy it to /etc/nodx/agent.yaml (if target absent) and use it.
	// Best-effort, never fails boot if USB missing; stick is air-gapped.
	const usbPath = "/media/usb/nodx-config.yaml"
	if _, err := os.Stat(DefaultConfigPath); os.IsNotExist(err) {
		if usb, err := os.ReadFile(usbPath); err == nil {
			_ = os.MkdirAll("/etc/nodx", 0755)
			if _, err := os.Stat(DefaultConfigPath); os.IsNotExist(err) {
				_ = os.WriteFile(DefaultConfigPath, usb, 0600)
			}
			return DefaultConfigPath, false
		}
	}
	return DefaultConfigPath, false
}

// parseFlatYAML parses a minimal flat `key: value` YAML subset (stdlib-only:
// no YAML library). Comments (#), blank lines, and surrounding quotes are
// handled. Nested maps/lists are rejected — the agent config is flat by design.
func parseFlatYAML(raw string) (map[string]string, error) {
	values := make(map[string]string)
	for i, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("line %d: expected `key: value`, got %q", i+1, line)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		if key == "" {
			return nil, fmt.Errorf("line %d: empty key", i+1)
		}
		value = strings.TrimSpace(strings.TrimSpace(value))
		// Strip an inline comment on unquoted values ("url # comment").
		if !strings.HasPrefix(value, `"`) && !strings.HasPrefix(value, `'`) {
			if idx := strings.Index(value, " #"); idx >= 0 {
				value = strings.TrimSpace(value[:idx])
			}
		}
		value = unquote(value)
		values[key] = value
	}
	return values, nil
}

func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

func applyMap(c *Config, values map[string]string) error {
	for key, value := range values {
		var err error
		switch key {
		case "server":
			c.ServerURL = value
		case "agent_secret":
			c.AgentSecret = value
		case "agent_id":
			c.AgentID = value
		case "agent_auth_token":
			c.AgentAuthToken = value
		case "datacenter_id":
			c.DatacenterID = value
		case "state_path":
			c.StatePath = value
		case "spool_dir":
			c.SpoolDir = value
		case "obs_addr":
			c.ObsAddr = value
		case "poll_interval":
			c.PollInterval, err = time.ParseDuration(value)
		case "heartbeat_interval":
			c.HeartbeatInterval, err = time.ParseDuration(value)
		case "request_timeout":
			c.RequestTimeout, err = time.ParseDuration(value)
		case "redfish_enabled":
			c.RedfishEnabled, err = strconv.ParseBool(value)
		case "batch_max_samples":
			c.BatchMaxSamples, err = strconv.Atoi(value)
		case "batch_max_bytes":
			c.BatchMaxBytes, err = strconv.Atoi(value)
		case "spool_max_bytes":
			var n int64
			n, err = strconv.ParseInt(value, 10, 64)
			c.SpoolMaxBytes = n
		case "spool_max_files":
			c.SpoolMaxFiles, err = strconv.Atoi(value)
		default:
			return fmt.Errorf("unknown key %q (typo? config is flat `key: value` only)", key)
		}
		if err != nil {
			return fmt.Errorf("key %q: invalid value %q: %w", key, value, err)
		}
	}
	return nil
}

// overlayEnv applies env > YAML: every set variable wins over the file.
func overlayEnv(c *Config) error {
	var err error
	set := func(key string, apply func(string) error) error {
		if v, ok := os.LookupEnv(key); ok {
			if err := apply(v); err != nil {
				return fmt.Errorf("%s: %w", key, err)
			}
		}
		return nil
	}
	str := func(dst *string) func(string) error {
		return func(v string) error { *dst = v; return nil }
	}
	if err = set("NODX_SERVER", str(&c.ServerURL)); err != nil {
		return err
	}
	if err = set("NODX_AGENT_SECRET", str(&c.AgentSecret)); err != nil {
		return err
	}
	if err = set("AGENT_ID", str(&c.AgentID)); err != nil {
		return err
	}
	if err = set("AGENT_AUTH_TOKEN", str(&c.AgentAuthToken)); err != nil {
		return err
	}
	if err = set("DATACENTER_ID", str(&c.DatacenterID)); err != nil {
		return err
	}
	if err = set("NODX_AGENT_STATE", str(&c.StatePath)); err != nil {
		return err
	}
	if err = set("NODX_AGENT_SPOOL_DIR", str(&c.SpoolDir)); err != nil {
		return err
	}
	if err = set("NODX_OBS_ADDR", str(&c.ObsAddr)); err != nil {
		return err
	}
	dur := func(dst *time.Duration) func(string) error {
		return func(v string) error {
			d, err := time.ParseDuration(v)
			if err != nil {
				return err
			}
			*dst = d
			return nil
		}
	}
	if err = set("NODX_POLL_INTERVAL", dur(&c.PollInterval)); err != nil {
		return err
	}
	if err = set("NODX_HEARTBEAT_INTERVAL", dur(&c.HeartbeatInterval)); err != nil {
		return err
	}
	if err = set("NODX_REQUEST_TIMEOUT", dur(&c.RequestTimeout)); err != nil {
		return err
	}
	if err = set("NODX_REDFISH_ENABLED", func(v string) error {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return err
		}
		c.RedfishEnabled = b
		return nil
	}); err != nil {
		return err
	}
	return nil
}

// Validate enforces the fail-fast contract: missing/unparseable identity,
// endpoint, or timing values refuse boot. No auto-fabricated UUIDs, no
// credential defaults.
func (c Config) Validate() error {
	if c.ServerURL == "" {
		return fmt.Errorf("config: NODX_SERVER is not set -- refusing to start without an ingestion endpoint")
	}
	u, err := url.Parse(c.ServerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return fmt.Errorf("config: NODX_SERVER %q is not a valid http(s) URL", c.ServerURL)
	}
	if c.AgentSecret == "" {
		return fmt.Errorf("config: NODX_AGENT_SECRET is not set -- refusing to start without agent handshake secret")
	}
	if c.AgentID == "" {
		return fmt.Errorf("config: AGENT_ID is not set -- agent identity is human-claimed via the pairing endpoint, never auto-generated")
	}
	if !IsUUID(c.AgentID) {
		return fmt.Errorf("config: AGENT_ID %q is not a UUID -- refusing to boot with a fabricated identity", c.AgentID)
	}
	if c.AgentAuthToken == "" {
		return fmt.Errorf("config: AGENT_AUTH_TOKEN is not set -- refusing to start without the pairing claim token")
	}
	if c.DatacenterID == "" {
		return fmt.Errorf("config: DATACENTER_ID is not set -- refusing to start without placement identity")
	}
	if c.RedfishEnabled {
		return fmt.Errorf("config: redfish_enabled=true is not supported in R26 -- on-agent Redfish is deferred, use the redfish-collector service")
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("config: poll_interval must be > 0, got %s", c.PollInterval)
	}
	if c.HeartbeatInterval <= 0 {
		return fmt.Errorf("config: heartbeat_interval must be > 0, got %s", c.HeartbeatInterval)
	}
	if c.RequestTimeout <= 0 {
		return fmt.Errorf("config: request_timeout must be > 0, got %s", c.RequestTimeout)
	}
	if c.StatePath == "" {
		return fmt.Errorf("config: state_path is empty")
	}
	if c.SpoolDir == "" {
		return fmt.Errorf("config: spool_dir is empty")
	}
	if _, _, err := net.SplitHostPort(c.ObsAddr); err != nil {
		return fmt.Errorf("config: obs_addr %q is not host:port: %w", c.ObsAddr, err)
	}
	if c.BatchMaxSamples < 1 {
		return fmt.Errorf("config: batch_max_samples must be >= 1, got %d", c.BatchMaxSamples)
	}
	if c.BatchMaxBytes < 512 {
		return fmt.Errorf("config: batch_max_bytes must be >= 512, got %d", c.BatchMaxBytes)
	}
	if c.SpoolMaxBytes < 1 {
		return fmt.Errorf("config: spool_max_bytes must be > 0")
	}
	if c.SpoolMaxFiles < 1 {
		return fmt.Errorf("config: spool_max_files must be >= 1, got %d", c.SpoolMaxFiles)
	}
	return nil
}

// ValidateDryRun enforces only what `--stdout` actually consumes: a usable
// agent identity (NewSample rejects non-UUID agent_id, dropping every
// sample) and a positive poll interval. Everything tied to delivery --
// endpoint, secrets, claim token, placement, state path, spool, observability
// address -- is intentionally absent: a dry run never dials, never persists,
// and never binds a port. Full delivery requirements live in Validate.
func (c Config) ValidateDryRun() error {
	if c.AgentID == "" {
		return fmt.Errorf("config: AGENT_ID is empty after dry-run defaulting")
	}
	if !IsUUID(c.AgentID) {
		return fmt.Errorf("config: AGENT_ID %q is not a UUID -- dry-run refuses a fabricated identity", c.AgentID)
	}
	if c.PollInterval <= 0 {
		return fmt.Errorf("config: poll_interval must be > 0, got %s", c.PollInterval)
	}
	return nil
}

// IsUUID mirrors the ingestion edge validation: 8-4-4-4-12 hex with dashes.
// Device and agent IDs flow into UUID columns; anything else dies as a queue
// drop downstream, so we refuse it at boot instead.
func IsUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		ch := s[i]
		switch i {
		case 8, 13, 18, 23:
			if ch != '-' {
				return false
			}
		default:
			if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f' || ch >= 'A' && ch <= 'F') {
				return false
			}
		}
	}
	return true
}
