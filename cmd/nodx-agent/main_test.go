package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nodx-ops/nodx-agent/internal/collect"
)

// drainStdout must emit one valid, contract-shaped JSON object per line.
// This is the acceptance proof for `--stdout`: if a sample cannot round-trip
// through this path, a contributor sees nothing, and the demo is worthless.
func TestDrainStdoutEmitsOneEnvelopePerLine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	samples := []collect.Sample{
		{DeviceID: "d1", Metric: "cpu_load_1m", Value: 0.42, Timestamp: 1700000000, AgentID: "a1b2c3d4-1111-4111-8111-000000000001", Protocol: "agent"},
		{DeviceID: "d1", Metric: "mem_available_bytes", Value: 8589934592, Timestamp: 1700000015, AgentID: "a1b2c3d4-1111-4111-8111-000000000001", Protocol: "agent"},
	}
	in := make(chan collect.Sample, len(samples))
	for _, s := range samples {
		in <- s
	}

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- drainStdout(ctx, &buf, in) }()

	// Cancel once both lines are out (drain returns on ctx.Done).
	deadline := time.After(3 * time.Second)
	for strings.Count(buf.String(), "\n") < len(samples) {
		select {
		case <-deadline:
			t.Fatalf("timed out; got %q", buf.String())
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}
	cancel()

	if err := <-done; err != nil {
		t.Fatalf("drainStdout: %v", err)
	}

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != len(samples) {
		t.Fatalf("want %d lines, got %d: %q", len(samples), len(lines), buf.String())
	}
	for i, line := range lines {
		var got collect.Sample
		if err := json.Unmarshal([]byte(line), &got); err != nil {
			t.Fatalf("line %d is not valid JSON: %v (%q)", i, err, line)
		}
		if got != samples[i] {
			t.Fatalf("line %d round-trip mismatch:\n want %+v\n  got %+v", i, samples[i], got)
		}
		if got.Metric == "" || got.AgentID == "" || got.Protocol == "" {
			t.Fatalf("line %d missing contract fields: %+v", i, got)
		}
	}
}

// A broken writer (e.g. `nodx-agent --stdout | head -1` closing the pipe)
// must surface as an error, not panic or spin silently.
func TestDrainStdoutReportsWriteFailure(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	in := make(chan collect.Sample, 1)
	in <- collect.Sample{DeviceID: "d", Metric: "m", Value: 1, Timestamp: 1, AgentID: "a1b2c3d4-1111-4111-8111-000000000001", Protocol: "agent"}

	err := drainStdout(ctx, failingWriter{}, in)
	if err == nil {
		t.Fatal("expected an error from a failing writer, got nil")
	}
	if !strings.Contains(err.Error(), "encode sample") {
		t.Fatalf("expected encode error, got: %v", err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, context.DeadlineExceeded }

// Cancellation must terminate promptly rather than block forever on an idle
// channel -- the agent's shutdown path depends on this returning.
func TestDrainStdoutStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	in := make(chan collect.Sample) // never written: only ctx can end the loop

	done := make(chan error, 1)
	go func() { done <- drainStdout(ctx, &bufZero{}, in) }()
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean cancel must not error, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("drainStdout did not return after cancel")
	}
}

type bufZero struct{}

func (*bufZero) Write(p []byte) (int, error) { return len(p), nil }
