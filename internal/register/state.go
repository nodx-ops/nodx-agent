// Package register implements the R26 verify-only registration path.
//
// There is NO pairing POST from the agent (no server counterpart exists):
// a human claims the agent out-of-band (curls the pairing endpoint with a
// JWT) and injects AGENT_ID + AGENT_AUTH_TOKEN + NODX_SERVER +
// NODX_AGENT_SECRET + DATACENTER_ID into the agent's environment.
//
// At boot the agent loads its state file and verifies the claimed identity:
//   - first boot persists the claim (0600, fsync'd);
//   - a stored agent_id that differs from the claim is fatal (re-claiming
//     requires an operator to remove the state file);
//   - 401/403 from the server is a log.Fatal operator error, never looped
//     (enforced in internal/deliver).
//
// No UUID is ever auto-fabricated.
package register

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/nodx-ops/nodx-agent/internal/config"
)

// State is the persisted first-boot claim.
type State struct {
	AgentID       string `json:"agent_id"`
	DatacenterID  string `json:"datacenter_id"`
	FirstSeenUnix int64  `json:"first_seen_unix"`
}

// Verify loads the state at path and checks it against the claimed identity.
// Missing state is persisted (first boot); any mismatch or corruption is an
// error the caller must treat as fatal.
func Verify(path, agentID, datacenterID string) (State, error) {
	if !config.IsUUID(agentID) {
		return State{}, fmt.Errorf("register: AGENT_ID %q is not a UUID -- refusing to boot with a fabricated identity", agentID)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			return State{}, fmt.Errorf("register: read state %s: %w", path, err)
		}
		st := State{AgentID: agentID, DatacenterID: datacenterID, FirstSeenUnix: time.Now().Unix()}
		if err := persist(path, st); err != nil {
			return State{}, err
		}
		log.Printf("[REGISTER] first boot: persisted agent claim to %s (0600)", path)
		return st, nil
	}
	var st State
	if err := json.Unmarshal(raw, &st); err != nil {
		return State{}, fmt.Errorf("register: corrupt state %s (re-claim requires operator removal): %w", path, err)
	}
	if !config.IsUUID(st.AgentID) {
		return State{}, fmt.Errorf("register: corrupt state %s: stored agent_id is not a UUID (re-claim requires operator removal)", path)
	}
	if st.AgentID != agentID {
		return State{}, fmt.Errorf("register: state agent_id mismatch (stored %s, claimed %s) -- refusing to start; re-claim requires operator removal of %s",
			st.AgentID, agentID, path)
	}
	if st.DatacenterID != datacenterID {
		return State{}, fmt.Errorf("register: datacenter mismatch (stored %s, claimed %s) -- refusing to start; re-claim requires operator removal of %s",
			st.DatacenterID, datacenterID, path)
	}
	// The state file carries the claim token's sibling identity: repair lax
	// permissions instead of failing (the content is already verified above).
	if fi, err := os.Stat(path); err == nil && fi.Mode().Perm()&0o077 != 0 {
		log.Printf("[WARN] tightening state file permissions to 0600: %s", path)
		if err := os.Chmod(path, 0o600); err != nil {
			return State{}, fmt.Errorf("register: chmod state %s: %w", path, err)
		}
	}
	return st, nil
}

// persist writes the claim atomically-ish (0600, fsync, dir-sync) so a crash
// mid-boot cannot leave a half-written identity behind.
func persist(path string, st State) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("register: create state dir: %w", err)
	}
	raw, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("register: encode state: %w", err)
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("register: write state %s: %w", path, err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return fmt.Errorf("register: write state %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("register: fsync state %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("register: close state %s: %w", path, err)
	}
	// Mode applies only at create; enforce 0600 on pre-existing files too.
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("register: chmod state %s: %w", path, err)
	}
	if dir, err := os.Open(filepath.Dir(path)); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
