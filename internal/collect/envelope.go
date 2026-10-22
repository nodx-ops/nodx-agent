// Package collect gathers hardware telemetry from fresh local readers
// (sysfs, IPMI, GPU) and emits canonical ingestion envelopes.
//
// The envelope mirrors the ingestion edge contract:
// {device_id UUID, metric, value, timestamp epoch, agent_id agents.id UUID,
// protocol}. Partial emission is the rule: a failed source yields a
// collect_error_<source> marker, never a crash.
package collect

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"math"
	"time"
)

// ProtocolTag identifies agent-originated samples to the worker.
const ProtocolTag = "agent"

// Sample is one canonical telemetry point.
type Sample struct {
	DeviceID  string  `json:"device_id"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
	AgentID   string  `json:"agent_id"`
	Protocol  string  `json:"protocol,omitempty"`
}

// metricPair is a pre-validated metric ready for envelope creation.
type metricPair struct {
	metric string
	value  float64
}

func pair(metric string, value float64) metricPair { return metricPair{metric, value} }

// DeviceID derives the agent's single device identity: uuidV5(agentID, "self").
// Per-component device IDs are reserved for later; R26 ships one device.
func DeviceID(agentID string) string { return uuidV5(agentID, "self") }

// NewSample stamps a validated envelope. ts is epoch seconds (0 = now).
// Bounds mirror the mqtt-collector validation: finite values only, timestamps
// inside [now-24h, now+5m]. Invalid input is a counted skip, never a send.
func NewSample(agentID, deviceID, metric string, value float64, ts int64) (Sample, error) {
	if !isUUID(agentID) {
		return Sample{}, fmt.Errorf("collect: agent_id is not a UUID")
	}
	if !isUUID(deviceID) {
		return Sample{}, fmt.Errorf("collect: device_id is not a UUID")
	}
	if !validMetric(metric) {
		return Sample{}, fmt.Errorf("collect: invalid metric name %q", metric)
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return Sample{}, fmt.Errorf("collect: non-finite value for %s", metric)
	}
	now := time.Now().Unix()
	if ts == 0 {
		ts = now
	}
	if ts < now-24*3600 || ts > now+5*60 {
		return Sample{}, fmt.Errorf("collect: timestamp %d outside [now-24h, now+5m]", ts)
	}
	return Sample{
		DeviceID: deviceID, Metric: metric, Value: value,
		Timestamp: ts, AgentID: agentID, Protocol: ProtocolTag,
	}, nil
}

func validMetric(m string) bool {
	if m == "" || len(m) > 128 {
		return false
	}
	for i := 0; i < len(m); i++ {
		if m[i] <= 0x20 || m[i] == 0x7f {
			return false
		}
	}
	return true
}

// isUUID mirrors the ingestion edge validation: 8-4-4-4-12 hex with dashes.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < 36; i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// uuidV5 derives a deterministic RFC 4122 v5 UUID from a namespace + name,
// mirroring the redfish-collector shape (namespace as raw string bytes).
func uuidV5(namespace, name string) string {
	h := sha1.New()
	h.Write([]byte(namespace))
	h.Write([]byte(name))
	sum := h.Sum(nil)
	sum[6] = (sum[6] & 0x0f) | 0x50 // version 5
	sum[8] = (sum[8] & 0x3f) | 0x80 // RFC 4122 variant
	b := make([]byte, 36)
	hex.Encode(b[0:8], sum[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], sum[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], sum[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], sum[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], sum[10:16])
	return string(b)
}
