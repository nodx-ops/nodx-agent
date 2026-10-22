package collect

import (
	"context"
	"expvar"
	"log"
	"math/rand/v2"
	"os/exec"
	"syscall"
	"time"
)

// QueueDepth bounds the collect→deliver handoff. Overflow drops the NEWEST
// sample with a counted drop: deliver is behind, so fresher data arriving
// next tick is worth more than a stale backlog.
const QueueDepth = 512

// defaultCmdTimeout bounds every subprocess (ipmitool, nvidia-smi).
const defaultCmdTimeout = 10 * time.Second

var (
	varCollected     = expvar.NewInt("samples_collected_total")
	varQueueDrops    = expvar.NewInt("queue_drops_total")
	varCollectErrors = expvar.NewInt("collect_errors_total")
	varPollDuration  = expvar.NewInt("poll_duration_ms")
)

// sourceReader is one hardware source. read returns gathered pairs; a
// non-nil error means the whole source failed (collector emits a
// collect_error_<name> marker). Per-file skips inside a source are partial
// emission, not failure. Implementations must never spawn goroutines.
type sourceReader interface {
	name() string
	read(now time.Time) ([]metricPair, error)
}

// Collector runs the single poll loop: immediate first poll, then tick with
// ±10% jitter. Synchronous polling in one goroutine — never spawn per tick
// (overlapping generations under stall).
type Collector struct {
	agentID  string
	deviceID string
	readers  []sourceReader
	interval time.Duration
	out      chan Sample
}

// New wires the fresh readers in precedence order. Metric names are disjoint
// across readers by construction; pollOnce additionally dedupes first-wins.
func New(agentID string, interval time.Duration) *Collector {
	return &Collector{
		agentID:  agentID,
		deviceID: DeviceID(agentID),
		readers: []sourceReader{
			newSysfsReader(),
			newIPMIReader(defaultCmdTimeout),
			newGPUReader(defaultCmdTimeout),
		},
		interval: interval,
		out:      make(chan Sample, QueueDepth),
	}
}

// Out exposes the bounded handoff channel (read-only to deliver).
func (c *Collector) Out() <-chan Sample { return c.out }

// DeviceID returns the agent's single device identity.
func (c *Collector) DeviceID() string { return c.deviceID }

func (c *Collector) Run(ctx context.Context) {
	timer := time.NewTimer(0) // immediate first poll
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Printf("[SHUTDOWN] collector stopped")
			return
		case <-timer.C:
			c.pollOnce(time.Now())
			timer.Reset(jitterInterval(c.interval))
		}
	}
}

func (c *Collector) pollOnce(now time.Time) {
	start := time.Now()
	seen := make(map[string]bool)
	for _, r := range c.readers {
		pairs, err := r.read(now)
		if err != nil {
			log.Printf("[WARN] collect %s failed: %v (partial emit)", r.name(), err)
			varCollectErrors.Add(1)
			c.emit(pair("collect_error_"+r.name(), 1), now.Unix())
			continue
		}
		for _, p := range pairs {
			if seen[p.metric] {
				continue // first-reader-wins: names are disjoint, this is belt-and-braces
			}
			seen[p.metric] = true
			c.emit(p, now.Unix())
		}
	}
	varPollDuration.Set(time.Since(start).Milliseconds())
}

func (c *Collector) emit(p metricPair, ts int64) {
	s, err := NewSample(c.agentID, c.deviceID, p.metric, p.value, ts)
	if err != nil {
		varCollectErrors.Add(1)
		return
	}
	c.Emit(s)
}

// Emit enqueues an externally-built sample (heartbeat path) under the same
// drop-newest + counted-drop discipline as polled samples.
func (c *Collector) Emit(s Sample) bool {
	select {
	case c.out <- s:
		varCollected.Add(1)
		return true
	default:
		varQueueDrops.Add(1)
		return false
	}
}

// jitterInterval spreads the fleet's ticks: interval * [0.9, 1.1).
func jitterInterval(d time.Duration) time.Duration {
	return time.Duration(float64(d) * (0.9 + 0.2*rand.Float64()))
}

// runCmd executes a helper binary with a kill timeout and process-group kill:
// exec.CommandContext kills only the direct child, so on timeout the whole
// group is SIGKILLed (grandchild safety). Stdout is captured; stderr is
// discarded (helpers are chatty on missing hardware).
func runCmd(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	return out, err
}
