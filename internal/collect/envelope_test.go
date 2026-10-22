package collect

import (
	"math"
	"strings"
	"testing"
	"time"
)

const testAgent = "a1b2c3d4-1111-4111-8111-000000000001"

func TestUUIDV5Shape(t *testing.T) {
	a := uuidV5(testAgent, "self")
	b := uuidV5(testAgent, "self")
	if a != b {
		t.Fatalf("uuidV5 not deterministic: %s vs %s", a, b)
	}
	if len(a) != 36 {
		t.Fatalf("uuidV5 length = %d, want 36", len(a))
	}
	for _, i := range []int{8, 13, 18, 23} {
		if a[i] != '-' {
			t.Fatalf("uuidV5 missing dash at %d: %s", i, a)
		}
	}
	if a[14] != '5' {
		t.Fatalf("uuidV5 version nibble = %c, want 5: %s", a[14], a)
	}
	if !strings.ContainsRune("89ab", rune(a[19])) {
		t.Fatalf("uuidV5 variant = %c, want one of 89ab: %s", a[19], a)
	}
	if got := uuidV5(testAgent, "other"); got == a {
		t.Fatalf("uuidV5 ignores name: %s", got)
	}
	if got := uuidV5("b1b2c3d4-1111-4111-8111-000000000002", "self"); got == a {
		t.Fatalf("uuidV5 ignores namespace: %s", got)
	}
	if !isUUID(DeviceID(testAgent)) {
		t.Fatalf("DeviceID %q fails UUID validation", DeviceID(testAgent))
	}
}

func TestNewSample(t *testing.T) {
	now := time.Now().Unix()
	device := DeviceID(testAgent)
	cases := []struct {
		name               string
		agent, dev, metric string
		value              float64
		ts                 int64
		wantErr            string
	}{
		{"valid explicit ts", testAgent, device, "power_draw_watts", 142.5, now, ""},
		{"zero ts means now", testAgent, device, "power_draw_watts", 1, 0, ""},
		{"empty metric", testAgent, device, "", 1, now, "invalid metric"},
		{"metric with space", testAgent, device, "bad metric", 1, now, "invalid metric"},
		{"NaN rejected", testAgent, device, "m", math.NaN(), now, "non-finite"},
		{"+Inf rejected", testAgent, device, "m", math.Inf(1), now, "non-finite"},
		{"-Inf rejected", testAgent, device, "m", math.Inf(-1), now, "non-finite"},
		{"stale ts", testAgent, device, "m", 1, now - 25*3600, "outside"},
		{"future ts", testAgent, device, "m", 1, now + 6*60, "outside"},
		{"bad agent id", "agent-1", device, "m", 1, now, "agent_id"},
		{"bad device id", testAgent, "dev-1", "m", 1, now, "device_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := NewSample(tc.agent, tc.dev, tc.metric, tc.value, tc.ts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected valid, got %v", err)
				}
				if s.Protocol != ProtocolTag {
					t.Fatalf("protocol = %q, want %q", s.Protocol, ProtocolTag)
				}
				if s.AgentID != tc.agent || s.Metric != tc.metric {
					t.Fatalf("envelope identity not stamped: %+v", s)
				}
				if s.Timestamp < now-5 || s.Timestamp > now+5*60+5 {
					t.Fatalf("timestamp not sane: %d", s.Timestamp)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error containing %q, got %+v", tc.wantErr, s)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("expected error containing %q, got %q", tc.wantErr, err.Error())
			}
		})
	}
}
