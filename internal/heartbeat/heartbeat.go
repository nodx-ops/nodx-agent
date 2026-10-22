// Package heartbeat emits the agent's liveness markers on the 60s tick.
//
// Two metrics carry identity: agent.heartbeat (=1, the worker-visible pulse)
// and agent_version (=1, inert marker — the string version rides expvar,
// the startup log, and the delivery User-Agent; R37 owns version semantics).
// Spool depth rides along as gauges so a silent disk-full partition is
// visible in telemetry itself, not just in expvar.
package heartbeat

import (
	"context"
	"expvar"
	"log"
	"time"

	"github.com/nodx-ops/nodx-agent/internal/collect"
)

// Version is the agent release. R37 owns upgrades; until then this is
// inert information (no auto-update machinery reads it).
const Version = "0.1.0"

var varBeats = expvar.NewInt("heartbeat_total")

// Emitter enqueues one sample toward deliver (false = queue-full drop,
// already counted by the collector).
type Emitter func(s collect.Sample) bool

// Depth reports current spool depth for the gauges.
type Depth func() (files int, bytes int64)

// Build assembles one heartbeat's samples. Construction cannot fail: metrics
// are constants, values are finite, timestamps are now.
func Build(agentID, deviceID string, nowUnix int64, files int, bytes int64) []collect.Sample {
	mk := func(metric string, value float64) *collect.Sample {
		s, err := collect.NewSample(agentID, deviceID, metric, value, nowUnix)
		if err != nil {
			return nil
		}
		return &s
	}
	var out []collect.Sample
	for _, p := range []struct {
		metric string
		value  float64
	}{
		{"agent.heartbeat", 1},
		{"agent_version", 1},
		{"agent.spool_files", float64(files)},
		{"agent.spool_bytes", float64(bytes)},
	} {
		if s := mk(p.metric, p.value); s != nil {
			out = append(out, *s)
		}
	}
	return out
}

// Run beats immediately (so a fresh agent is visible without waiting a full
// interval) and then on every tick until ctx ends.
func Run(ctx context.Context, interval time.Duration, agentID, deviceID string, depth Depth, emit Emitter) {
	beat := func() {
		files, bytes := depth()
		for _, s := range Build(agentID, deviceID, time.Now().Unix(), files, bytes) {
			emit(s)
		}
		varBeats.Add(1)
	}
	beat()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[SHUTDOWN] heartbeat stopped")
			return
		case <-ticker.C:
			beat()
		}
	}
}
