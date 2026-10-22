// Command nodx-agent is the R26 deployment agent: first-boot registration
// (verify-only), hardware collection (sysfs/IPMI/GPU), resilient delivery
// with file spool, and a 60s heartbeat. One binary, three config profiles.
//
// Supervisor shape: fail-fast config → state verify → NotifyContext root →
// four goroutines (collect, heartbeat, deliver, loopback observability) →
// readiness-503 → drain → Shutdown(15s) → Wait.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"expvar"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/nodx-ops/nodx-agent/internal/collect"
	"github.com/nodx-ops/nodx-agent/internal/config"
	"github.com/nodx-ops/nodx-agent/internal/deliver"
	"github.com/nodx-ops/nodx-agent/internal/heartbeat"
	"github.com/nodx-ops/nodx-agent/internal/register"
)

// lingerDur batches sub-second sample arrivals so a poll wave ships as one POST.
const lingerDur = 2 * time.Second

// finalFlushTimeout bounds the shutdown drain (inside the 15s server Shutdown).
const finalFlushTimeout = 10 * time.Second

func main() {
	cfgPath := flag.String("config", "", "agent YAML path (default $NODX_AGENT_CONFIG or /etc/nodx/agent.yaml)")
	stdoutMode := flag.Bool("stdout", false, "dry-run: collect local hardware and print one JSON envelope per line to stdout; no server, no state file, no spool")
	flag.Parse()

	if *stdoutMode {
		runStdout(*cfgPath)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
	if _, err := register.Verify(cfg.StatePath, cfg.AgentID, cfg.DatacenterID); err != nil {
		log.Fatalf("[FATAL] %v", err)
	}

	expvar.NewString("agent_version").Set(heartbeat.Version)

	spool, err := deliver.NewSpool(cfg.SpoolDir, cfg.SpoolMaxBytes, cfg.SpoolMaxFiles)
	if err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
	client := deliver.NewClient(cfg.ServerURL, cfg.AgentSecret, heartbeat.Version, cfg.RequestTimeout)
	col := collect.New(cfg.AgentID, cfg.PollInterval)

	var ready atomic.Bool
	ready.Store(true)
	obs := &http.Server{
		Addr:         cfg.ObsAddr,
		Handler:      obsHandler(&ready),
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}
	warnIfNonLoopback(cfg.ObsAddr)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	var wg sync.WaitGroup
	wg.Add(4)
	go func() { defer wg.Done(); col.Run(ctx) }()
	go func() {
		defer wg.Done()
		heartbeat.Run(ctx, cfg.HeartbeatInterval, cfg.AgentID, col.DeviceID(), spoolDepth(spool), col.Emit)
	}()
	go func() { defer wg.Done(); runDeliver(ctx, col.Out(), spool, client, cfg) }()
	go func() {
		defer wg.Done()
		log.Printf("[OBS] loopback listener on %s (/health /ready /debug/vars /debug/pprof)", cfg.ObsAddr)
		if err := obs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("[ERROR] observability listener collapsed: %v", err)
		}
	}()

	log.Printf("[START] nodx-agent %s agent=%s → %s (poll %s, heartbeat %s)",
		heartbeat.Version, cfg.AgentID, cfg.ServerURL, cfg.PollInterval, cfg.HeartbeatInterval)

	<-ctx.Done()
	log.Printf("[SHUTDOWN] SIGTERM/SIGINT received -- draining")
	ready.Store(false) // /ready flips to 503 first (honest stopped-signal)

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = obs.Shutdown(shutdownCtx) // deliver's final flush runs concurrently, bounded by finalFlushTimeout
	wg.Wait()
	log.Printf("[STOP] nodx-agent exited")
}

// runStdout is the `--stdout` dry-run: collect from local readers and write
// one canonical envelope per line to stdout, then stop on SIGINT/SIGTERM.
//
// It deliberately shares NONE of runDeliver's machinery. There is no spool
// (a failed local write is not a network event worth persisting), no backoff
// (nothing can be retryable when nothing dials), no observability listener,
// no heartbeat (it POSTs), and no state file. The envelope shape is not a
// second implementation: both paths encode collect.Sample, so the JSON tags
// that define the wire contract are the single source of truth for both.
//
// Logs go to stderr, so stdout stays machine-readable: nodx-agent -stdout | jq
func runStdout(cfgPath string) {
	cfg, err := config.LoadDryRun(cfgPath)
	if err != nil {
		log.Fatalf("[FATAL] %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	col := collect.New(cfg.AgentID, cfg.PollInterval)
	log.Printf("[DRY-RUN] nodx-agent %s -> stdout (agent=%s, poll=%s, no server/state/spool)",
		heartbeat.Version, cfg.AgentID, cfg.PollInterval)
	log.Printf("[DRY-RUN] expecting partial emission: sources absent on this host emit collect_error_* markers, not failures")

	go col.Run(ctx)

	if err := drainStdout(ctx, os.Stdout, col.Out()); err != nil {
		log.Fatalf("[FATAL] %v", err)
	}
	log.Printf("[DRY-RUN] stopped")
}

// drainStdout writes each collected sample as one JSON line until ctx is
// cancelled. It is separate from runDeliver on purpose (see runStdout):
// delivery semantics do not apply to a local file descriptor.
func drainStdout(ctx context.Context, w io.Writer, in <-chan collect.Sample) error {
	enc := json.NewEncoder(w)
	for {
		select {
		case <-ctx.Done():
			return nil
		case s := <-in:
			if err := enc.Encode(s); err != nil {
				return fmt.Errorf("stdout: encode sample: %w", err)
			}
		}
	}
}

// runDeliver batches the collect handoff, replays the spool oldest-first,
// and enforces the retry discipline: retryable failures spool + back off,
// 4xx drops are counted inside the client, 401/403 is fatal.
func runDeliver(ctx context.Context, in <-chan collect.Sample, spool *deliver.Spool, client *deliver.Client, cfg config.Config) {
	defer log.Printf("[SHUTDOWN] deliver stopped")
	bo := deliver.NewBackoff(deliver.DefaultBackoffMin, deliver.DefaultBackoffMax)
	var pending []collect.Sample

	linger := time.NewTimer(lingerDur)
	defer linger.Stop()
	resetLinger := func() {
		if !linger.Stop() {
			select {
			case <-linger.C:
			default:
			}
		}
		linger.Reset(lingerDur)
	}

	flush := func(sctx context.Context, final bool) {
		// Oldest-first replay: partition survivors send before fresh data.
		for {
			old, name, err := spool.Oldest()
			if err != nil {
				if !errors.Is(err, deliver.ErrSpoolEmpty) {
					log.Printf("[WARN] spool replay check: %v", err)
				}
				break
			}
			if serr := client.Send(sctx, old); serr != nil {
				if errors.Is(serr, deliver.ErrAuthFatal) {
					log.Fatalf("[FATAL] %v", serr)
				}
				log.Printf("[WARN] spooled batch held (%v); backing off", serr)
				if !final {
					_ = bo.Wait(ctx)
				}
				return
			}
			if rerr := spool.Remove(name); rerr != nil {
				log.Printf("[WARN] %v", rerr)
			}
			bo.Reset()
		}
		if len(pending) == 0 {
			return
		}
		chunks := deliver.CapBatch(pending, cfg.BatchMaxSamples, cfg.BatchMaxBytes)
		pending = nil
		for i, chunk := range chunks {
			if serr := client.Send(sctx, chunk); serr != nil {
				if errors.Is(serr, deliver.ErrAuthFatal) {
					log.Fatalf("[FATAL] %v", serr)
				}
				for _, rest := range chunks[i:] {
					if sterr := spool.Store(rest); sterr != nil {
						if errors.Is(sterr, deliver.ErrSpoolFull) {
							log.Printf("[WARN] spool at capacity: dropping %d samples (counted)", len(rest))
						} else {
							log.Printf("[WARN] spool store failed: %v (dropping %d samples)", sterr, len(rest))
						}
					}
				}
				log.Printf("[WARN] deliver failed (%v); spooled, backing off", serr)
				if !final {
					_ = bo.Wait(ctx)
				}
				return
			}
			bo.Reset()
		}
	}

	for {
		select {
		case <-ctx.Done():
			fctx, cancel := context.WithTimeout(context.Background(), finalFlushTimeout)
			flush(fctx, true)
			cancel()
			return
		case s := <-in:
			pending = append(pending, s)
			if len(pending) >= cfg.BatchMaxSamples {
				flush(ctx, false)
				resetLinger()
			}
		case <-linger.C:
			flush(ctx, false)
			linger.Reset(lingerDur)
		}
	}
}

// spoolDepth adapts the spool to the heartbeat gauge source.
func spoolDepth(spool *deliver.Spool) heartbeat.Depth {
	return func() (int, int64) {
		files, bytes, err := spool.Depth()
		if err != nil {
			log.Printf("[WARN] spool depth: %v", err)
			return 0, 0
		}
		return files, bytes
	}
}

// obsHandler serves liveness (/health), readiness (/ready, 503 while
// shutting down), and the stdlib introspection endpoints on loopback.
func obsHandler(ready *atomic.Bool) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"healthy"}`))
	})
	mux.HandleFunc("/ready", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"draining"}`))
			return
		}
		w.Write([]byte(`{"status":"ready"}`))
	})
	mux.Handle("/debug/vars", expvar.Handler())
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

// warnIfNonLoopback keeps the pprof-bearing listener honest: it is designed
// for loopback, and anything wider deserves an explicit operator decision.
func warnIfNonLoopback(addr string) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		log.Printf("[WARN] observability listener %s is not loopback: /debug/pprof is exposed beyond localhost", addr)
	}
}
