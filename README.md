# NodX Telemetry Agent

**A zero-dependency Go agent that reads hardware telemetry — CPU temperature, power draw, fan speeds, GPU metrics — from a Linux host and emits it as newline-delimited JSON.** No dependencies, no daemon sprawl, no phone-home: one static binary, stdlib only, roughly 2,300 lines.

```sh
git clone https://github.com/nodx-ops/nodx-agent.git
cd nodx-agent
go build -o nodx-agent ./cmd/nodx-agent
./nodx-agent --stdout
```

```json
{"device_id":"7b89d024-78b8-5ffd-80ae-6d6f18947477","metric":"cpu_load_1m","value":2.07,"timestamp":1792646719,"agent_id":"00000000-0000-5000-8000-000000000000","protocol":"agent"}
{"device_id":"7b88d024-78b8-5ffd-80ae-6d6f18947477","metric":"mem_used_bytes","value":857018368,"timestamp":1792646719,"agent_id":"00000000-0000-5000-8000-000000000000","protocol":"agent"}
```

Real output from a real machine, **with no server, no account, and no configuration**.

[![CI](https://github.com/nodx-ops/nodx-agent/actions/workflows/ci.yml/badge.svg)](https://github.com/nodx-ops/nodx-agent/actions/workflows/ci.yml)
[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.26-blue.svg)](https://go.dev/)
[![Dependencies](https://img.shields.io/badge/dependencies-0-brightgreen.svg)](#does-it-have-any-dependencies)

---

## Table of contents

- [What does this agent collect?](#what-does-this-agent-collect)
- [How do I run it?](#how-do-i-run-it)
- [Does it need a server?](#does-it-need-a-server)
- [How do I send telemetry to my own backend?](#how-do-i-send-telemetry-to-my-own-backend)
- [What does the telemetry envelope look like?](#what-does-the-telemetry-envelope-look-like)
- [How does the agent authenticate?](#how-does-the-agent-authenticate)
- [What happens when the network goes down?](#what-happens-when-the-network-goes-down)
- [Does it have any dependencies?](#does-it-have-any-dependencies)
- [How does it compare to Prometheus node_exporter?](#how-does-it-compare-to-prometheus-node_exporter)
- [How does it compare to Telegraf, Datadog Agent, and OpenTelemetry?](#how-does-it-compare-to-telegraf-datadog-agent-and-opentelemetry)
- [How do I install it as a service?](#how-do-i-install-it-as-a-service)
- [How do I run it air-gapped or on-premise?](#how-do-i-run-it-air-gapped-or-on-premise)
- [What is the project architecture?](#what-is-the-project-architecture)
- [How do I contribute?](#how-do-i-contribute)
- [Frequently asked questions](#frequently-asked-questions)

---

## What does this agent collect?

**It reads local hardware sensors from `/proc`, `/sys`, IPMI, and NVIDIA SMI, and emits each reading as a metric/value pair.**

| Metric | Source | What it means |
|---|---|---|
| `temperature_c` | `/sys/class/thermal` | CPU package / thermal zone temperature in °C |
| `power_draw_watts` | `/sys/class/hwmon` | Instantaneous power draw in watts |
| `powercap_draw_watts` | `/sys/class/powercap` | RAPL energy counters, CPU power cap draw |
| `fan_speed_rpm` | `/sys/class/hwmon` | Fan rotational speed in RPM |
| `bmc_temperature_c` | IPMI sensor | Baseboard management controller temperature |
| `cpu_load_1m` | `/proc/loadavg` | One-minute load average |
| `mem_used_bytes` | `/proc/meminfo` | Used memory in bytes |
| `mem_available_bytes` | `/proc/meminfo` | Available memory in bytes |

**Partial emission is deliberate.** If a source is missing — no `ipmitool`, no `nvidia-smi`, no thermal zones — the agent skips it and reports the gap rather than failing or inventing zeros. A container without `/proc` thermal data still emits `cpu_load_1m` and `mem_*`; you get the readings that exist, not an all-or-nothing error.

Absent sources are announced once on stderr:

```
[INFO] collect ipmi disabled: ipmitool not found (one-time)
[INFO] collect gpu disabled: nvidia-smi not found (one-time)
```

**Why these metrics?** Datacenter capacity planning needs thermal and power headroom, not just uptime. Knowing a rack draws 4.2 kW when its nameplate says 6 kW is the difference between a safe expansion and a tripped breaker.

---

## How do I run it?

**Clone, build, and pass `--stdout` — that is the complete quickstart.**

```sh
git clone https://github.com/nodx-ops/nodx-agent.git
cd nodx-agent
go build -o nodx-agent ./cmd/nodx-agent
./nodx-agent --stdout
```

`--stdout` is a **dry-run mode**: it collects from your machine and prints one JSON envelope per line instead of sending anything. No server, no credentials, no config file, no state file, no spool directory.

Stop it with `Ctrl-C`.

### Piping the output

Logs go to **stderr**; envelopes go to **stdout**. The two never interleave, so standard Unix tooling works:

```sh
# Pretty-print metrics
./nodx-agent --stdout 2>/dev/null | jq -r '"\(.metric) = \(.value)"'

# Only power readings
./nodx-agent --stdout 2>/dev/null | jq 'select(.metric | endswith("watts"))'

# Count metrics per collection cycle
./nodx-agent --stdout 2>/dev/null | jq -r .metric | sort | uniq -c

# Save to a file for later analysis
./nodx-agent --stdout 2>/dev/null > telemetry.jsonl
```

### Command-line flags

| Flag | Default | Purpose |
|---|---|---|
| `--stdout` | off | Dry-run: print envelopes to stdout, touch nothing else |
| `--config` | `$NODX_AGENT_CONFIG` or `/etc/nodx/agent.yaml` | Path to agent YAML |

### Do I need to set `AGENT_ID`?

**No.** In `--stdout` mode a fixed demo identity is used when none is supplied:

```
agent_id = 00000000-0000-5000-8000-000000000000
```

It is deliberately obvious, obviously synthetic, and used only for local display. Supplying a real `AGENT_ID` always overrides it — the demo default never replaces an identity you provided:

```sh
AGENT_ID="a1b2c3d4-1111-4111-8111-000000000001" ./nodx-agent --stdout
```

A malformed `AGENT_ID` is refused at boot with a clear error rather than silently producing no output.

---

## Does it need a server?

**Only if you want it to deliver somewhere. Collection itself is entirely local.**

| Mode | Needs a server? | What you get |
|---|---|---|
| `--stdout` | **No** | Real telemetry printed to your terminal |
| Delivery mode | Yes — any endpoint accepting the envelope | Telemetry stored wherever you point it |

There is no vendor lock-in and no phone-home. The agent posts to the URL you give it, and that is the whole of its network behaviour.

---

## How do I send telemetry to my own backend?

**Implement one HTTP endpoint. The agent speaks a single, small protocol, and you own everything downstream — storage, dashboards, alerting.**

The agent makes exactly **one** kind of outbound request:

```
POST {NODX_SERVER}/api/v1/telemetry
Authorization: Bearer {NODX_AGENT_SECRET}
Content-Type: application/json

{ single JSON envelope }
```

That is the entire surface area. There is no discovery handshake, no registration call from the agent, no batch framing to reverse-engineer.

### A complete reference receiver

Go — stdlib only, matching the agent's own philosophy:

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
)

type Envelope struct {
	DeviceID  string  `json:"device_id"`
	Metric    string  `json:"metric"`
	Value     float64 `json:"value"`
	Timestamp int64   `json:"timestamp"`
	AgentID   string  `json:"agent_id"`
	Protocol  string  `json:"protocol"`
}

func main() {
	secret := os.Getenv("AGENT_SECRET")
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/telemetry", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		auth := r.Header.Get("Authorization")
		if secret != "" && auth != "Bearer "+secret {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, `{"error":"read"}`, http.StatusBadRequest)
			return
		}
		var e Envelope
		if err := json.Unmarshal(body, &e); err != nil || e.Metric == "" {
			// Reject anything malformed: 4xx tells the agent to drop it
			// rather than retry forever.
			http.Error(w, `{"error":"bad envelope"}`, http.StatusBadRequest)
			return
		}
		fmt.Printf("%s %s=%v\n", e.AgentID, e.Metric, e.Value)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"accepted"}`))
	})
	log.Println("listening on :8080")
	log.Fatal(http.ListenAndServe("127.0.0.1:8080", mux))
}
```

Run it, then point the agent at it:

```sh
NODX_SERVER=http://127.0.0.1:8080 \
NODX_AGENT_SECRET=changeme \
AGENT_ID=a1b2c3d4-1111-4111-8111-000000000001 \
AGENT_AUTH_TOKEN=anything \
DATACENTER_ID=dc-1 \
./nodx-agent
```

You will see each envelope printed by your own receiver. From there, write to a file, SQLite, Postgres, Redis, Prometheus remote-write — the envelope does not care.

> **Delivery mode requires the four identity/endpoint values** (`NODX_SERVER`,
> `NODX_AGENT_SECRET`, `AGENT_ID`, `AGENT_AUTH_TOKEN`, `DATACENTER_ID`)
> because the agent refuses to boot with defaults or invented identity.
> `--stdout` deliberately does not require them.

---

## What does the telemetry envelope look like?

**One JSON object per POST, six fields, newline-delimited when printed to stdout.**

```json
{
  "device_id": "7b89d024-78b8-5ffd-80ae-6d6f18947477",
  "metric": "cpu_load_1m",
  "value": 2.07,
  "timestamp": 1792646719,
  "agent_id": "00000000-0000-5000-8000-000000000000",
  "protocol": "agent"
}
```

| Field | Type | Notes |
|---|---|---|
| `device_id` | UUID | Derived deterministically from `agent_id` (UUIDv5) |
| `metric` | string | 1–128 chars, no whitespace or control characters |
| `value` | number | **Must be finite** — `NaN` and `±Inf` are rejected before send |
| `timestamp` | integer | Epoch **seconds**, not milliseconds |
| `agent_id` | UUID | The claiming agent's identity |
| `protocol` | string | Always `"agent"` for this agent |

### Validation rules your receiver should mirror

The agent enforces these before sending, and a strict receiver should enforce them on arrival:

- `agent_id` and `device_id` must be valid UUIDs (`8-4-4-4-12` hex with dashes)
- `value` must be finite (no `NaN`, no `Infinity`)
- `timestamp` must fall within `[now − 24h, now + 5m]`
- `metric` must be non-empty, ≤128 chars, with no bytes ≤ `0x20` or `0x7f`
- **One object per request** — array payloads are rejected

### HTTP status codes and what the agent does

This table is the contract. Get it right and the agent behaves; get it wrong and it either drops data or stops entirely.

| Status | Agent behaviour |
|---|---|
| `2xx` | Accepted, counted, move on |
| `401`, `403` | **Fatal — process exits.** Credentials are wrong; retrying cannot fix that |
| `429`, `5xx` | Retryable — spooled to disk, exponential backoff, retried later |
| Other `4xx` | Dropped and counted. The payload is malformed; retrying would poison the queue forever |

Sending `401` for an envelope you merely *would prefer* to reject will kill the agent. Send `400`.

---

## How does the agent authenticate?

**With a static Bearer token in the `Authorization` header — nothing else.**

```
Authorization: Bearer {NODX_AGENT_SECRET}
```

There is no pairing request, no OAuth, no challenge-response initiated by the agent. Registration is **verify-only**: a human claims the agent identity out of band, and at first boot the agent persists that claim locally (`0600`) and refuses to start if the stored identity later disagrees with the configured one.

```
[REGISTER] first boot: persisted agent claim to /var/lib/nodx-agent/state.json (0600)
```

**Why verify-only?** An agent that can self-register can be impersonated by anything that finds it running. Claiming is a human action on the server side; the agent only proves it is the machine it claims to be.

`AGENT_AUTH_TOKEN` is stored but never transmitted by this agent.

---

## What happens when the network goes down?

**Telemetry is written to a local disk spool and replayed when the network returns. No samples are lost to a transient outage.**

| Condition | Behaviour |
|---|---|
| Connection refused, timeout, DNS failure | Batch spooled to disk, exponential backoff |
| Server returns `429` / `5xx` | Same — spooled and retried |
| Server returns `401` / `403` | Process exits immediately (fix credentials) |
| Server returns other `4xx` | Dropped and counted as malformed |
| Spool reaches capacity (50 MB / 10,000 files) | Oldest dropped, drop counted |

Spooling is **oldest-first on replay**, so a surviving backlog lands ahead of fresh samples and your dashboard sees a contiguous history rather than a gap followed by a burst.

Disk spool defaults: **50 MB**, **10,000 files**, at `/var/lib/nodx-agent/spool`.

---

## Does it have any dependencies?

**Zero. `go.mod` contains a module line and a Go version — nothing else.**

```go
module github.com/nodx-ops/nodx-agent

go 1.26
```

No third-party modules, no vendored code, no runtime agents, no sidecars. Every import is Go standard library or this module's own `internal/` packages.

**Why zero dependencies matters for infrastructure agents:**

- **Supply chain** — nothing to compromise upstream, nothing to audit per release
- **Air-gapped installs** — `go build` works with no module proxy and no network
- **Small blast radius** — a static binary, no shared libraries to break
- **Long-lived fleet** — no dependency churn forcing unplanned upgrades on machines you cannot easily reach

CI enforces this: the build fails if a `require` block ever appears in `go.mod`.

---

## How does it compare to Prometheus node_exporter?

**node_exporter exposes Prometheus metrics for scraping; this agent pushes JSON envelopes to an endpoint you define. Choose by pull versus push.**

| | NodX Agent | Prometheus node_exporter |
|---|---|---|
| Model | **Push** — agent POSTs to your endpoint | **Pull** — Prometheus scrapes `:9100` |
| Output format | One JSON object per POST | Prometheus text exposition |
| Dependencies | **None** (stdlib only) | None (single binary) |
| Needs a scrape target | No | Yes — Prometheus must reach it |
| NAT / egress-only networks | **Works** (outbound only) | Does not — needs inbound reach |
| Air-gapped install | **`go build`, no proxy needed** | Same |
| Rack-level power / BMC temps | **Yes** (IPMI, hwmon, RAPL) | Partial (hwmon, some IPMI) |
| Alerting | Your receiver decides | Alertmanager |
| Maturity | New | ~10 years, very widely deployed |

**Use node_exporter** if you already run Prometheus and want instant compatibility with the largest possible ecosystem.

**Use this agent** if you need outbound-only networking, want to own the receiving end, or are building your own storage and dashboards rather than adopting the Prometheus stack.

They are not mutually exclusive: run both, and let each serve its purpose.

---

## How does it compare to Telegraf, Datadog Agent, and OpenTelemetry?

**Those are multi-protocol collectors with plugin ecosystems; this is a deliberately small hardware agent you can read in an afternoon.**

| | NodX Agent | Telegraf | Datadog Agent | OTel Collector |
|---|---|---|---|---|
| Dependencies | **0** | Large plugin tree | Large | Large |
| Binary size | Small static | Larger | Large | Larger |
| Configuration | 4 values | Hundreds of plugins | SaaS-coupled | Pipelines, receivers, exporters |
| Hardware/DCIM focus | **Temperature, power, fans, BMC, GPU** | General | General | General |
| Vendor lock-in | **None** | None | Datadog backend | None |
| Extending it | Read ~2,300 lines | Write a plugin | Limited | Write a receiver/exporter |
| Best for | Owning your telemetry end to end | Breadth of inputs/outputs | Teams all-in on Datadog | Standardized OTLP pipelines |

**Choose Telegraf** when you need breadth — databases, message queues, webhooks — and are happy configuring dozens of plugins.

**Choose the Datadog Agent** when you have already bought Datadog and want the tightest integration with it.

**Choose the OpenTelemetry Collector** when your organization has standardized on OTLP and you want vendor-neutral telemetry plumbing.

**Choose this agent** when hardware telemetry is the point, when you want to read and modify the whole thing, or when your requirements are simple enough that a 2,300-line stdlib binary is genuinely the right size.

> An OTLP exporter would be a natural contribution — see
> [How do I contribute?](#how-do-i-contribute). It would need to preserve the
> stdlib-only rule, which is why it has not been added unilaterally.

---

## How do I install it as a service?

**Install the `.deb` or `.rpm`, supply your configuration, enable the unit.**

Pre-built packages are attached to every release:

```sh
# Debian / Ubuntu
sudo dpkg -i nodx-agent_*_linux_amd64.deb

# RHEL / Rocky / Alma
sudo rpm -i nodx-agent-*-1.x86_64.rpm
```

Then configure identity and endpoint:

```sh
sudo mkdir -p /etc/nodx
sudo tee /etc/nodx/agent.yaml >/dev/null <<'YAML'
server: https://your-endpoint.example.com
agent_id: a1b2c3d4-1111-4111-8111-000000000001
datacenter_id: dc-1
poll_interval: 15s
heartbeat_interval: 60s
YAML

sudo chmod 600 /etc/nodx/agent.yaml
```

Secrets do not belong in that file — supply them from the environment:

```sh
sudo systemctl edit nodx-agent
```

```ini
[Service]
Environment=NODX_AGENT_SECRET=your-secret-here
Environment=AGENT_AUTH_TOKEN=your-claim-token
```

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now nodx-agent
journalctl -u nodx-agent -f
```

### Configuration reference

Precedence is **environment > YAML file > defaults**.

| Variable | Default | Notes |
|---|---|---|
| `NODX_SERVER` | — | Required for delivery. `http(s)` URL |
| `NODX_AGENT_SECRET` | — | Required for delivery. Bearer token, **no default, ever** |
| `AGENT_ID` | — | Required. UUID; human-claimed, never auto-generated |
| `AGENT_AUTH_TOKEN` | — | Required for delivery. Stored, not transmitted |
| `DATACENTER_ID` | — | Required for delivery |
| `NODX_AGENT_CONFIG` | `/etc/nodx/agent.yaml` | Config file path |
| `NODX_AGENT_STATE` | `/var/lib/nodx-agent/state.json` | Persisted claim (`0600`) |
| `NODX_AGENT_SPOOL_DIR` | `/var/lib/nodx-agent/spool` | Disk spool |
| `NODX_POLL_INTERVAL` | `15s` | Collection period (±10% jitter) |
| `NODX_HEARTBEAT_INTERVAL` | `60s` | Heartbeat period |
| `NODX_REQUEST_TIMEOUT` | `10s` | Per-request timeout |
| `NODX_OBS_ADDR` | `127.0.0.1:6060` | Loopback observability listener |

**The agent fails fast rather than defaulting.** A missing endpoint or credential is a fatal boot error, not a silent fallback to a hardcoded value. Auto-generated identity is refused for the same reason.

### Observability endpoints

The agent serves a loopback-only listener (`127.0.0.1:6060`):

| Path | Purpose |
|---|---|
| `/health` | Liveness — `{"status":"healthy"}` |
| `/ready` | Readiness — `503` while draining on shutdown |
| `/debug/vars` | expvar counters (samples, drops, retries, spool depth) |
| `/debug/pprof/` | Standard Go pprof endpoints |

It warns loudly if bound beyond loopback, because pprof on a public interface is a gift to an attacker.

---

## How do I run it air-gapped or on-premise?

**The agent was designed for machines that cannot reach the internet.**

- **Build offline**: zero dependencies means `go build` succeeds with no module proxy. Cross-compile from a connected machine and copy the binary.
- **Packages are self-contained**: `.deb` and `.rpm` carry the binary and systemd unit, nothing else.
- **Delivery is outbound-only**: the agent initiates every connection, so a host behind NAT or an egress-only firewall works unchanged.
- **No phone-home**: the agent talks to the URL you configured and nothing else. FQDN allowlists can permit exactly one destination.

```sh
# Cross-compile for a disconnected x86 host
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o nodx-agent ./cmd/nodx-agent
```

The NodX platform ships separate SaaS, on-premise, and edge deployment tiers; this agent is the collection side of all three.

---

## What is the project architecture?

**One binary runs four concurrent goroutines under a single supervisor: collect, heartbeat, deliver, and a loopback observability server.**

```
                    ┌──────────────────────────────────────────┐
                    │  supervisor: fail-fast config → state    │
                    │  verify → NotifyContext root → drain     │
                    └──────────────────────────────────────────┘
                                       │
   ┌───────────────┬───────────────────┼───────────────────┬──────────────┐
   ▼               ▼                   ▼                   ▼              
collect         heartbeat           deliver            observability     
   │               │                   │                   │              
   │ sysfs         │ every 60s         │ linger 2s         │ 127.0.0.1:6060
   │ ipmi          │ POST heartbeat    │ batch → send      │ /health       
   │ gpu           │                   │ spool + backoff   │ /ready        
   │               ▼                   ▼                   │ /debug/vars   
   └────▶ chan ──────────────▶ HTTP POST /api/v1/telemetry  │ /debug/pprof 
           (depth 512,         Bearer auth, 1 envelope      └──────────────┘
            drop-newest)        per request                  
```

### Source layout

```
cmd/nodx-agent/        main: flag parsing, supervisor, runDeliver, --stdout
internal/
  collect/             sensors + envelope construction
    sysfs.go           /proc + /sys readers (thermal, hwmon, RAPL, load, mem)
    ipmi.go            ipmitool subprocess (10s kill timeout, process group)
    gpu.go             nvidia-smi subprocess
    envelope.go        Sample type + validation rules
    collector.go       poll loop, jitter, partial emission
  config/              env > YAML > defaults, fail-fast validation
  deliver/             HTTP client, disk spool, retry backoff, CapBatch
  heartbeat/           periodic heartbeat emission
  register/            verify-only first-boot claim persistence
packaging/             systemd unit + nfpm config + deployment profiles
```

### Design decisions worth knowing

| Decision | Why |
|---|---|
| **Drop-newest queue (depth 512)** | When delivery is behind, fresher data arriving next tick beats a stale backlog |
| **±10% poll jitter** | A fleet of agents ticking in lockstep would synchronise into a thundering herd |
| **Process-group kill on subprocess timeout** | `exec.CommandContext` kills only the direct child; a grandchild `ipmitool` would otherwise survive every timeout |
| **First-reader-wins dedup** | Metric names are disjoint by construction; dedup is belt-and-braces against a duplicate |
| **Partial emission, never crash** | A failed source yields a `collect_error_*` marker, never a dead agent |
| **15s default poll** | Fast enough for thermal incident detection, slow enough to be invisible on host load |

### Shutdown behaviour

On `SIGTERM`/`SIGINT`: readiness flips to `503` **first** (an honest stopped-signal, so a load balancer stops routing before data stops flowing), in-flight samples flush with a bounded 10s timeout, then the process exits within 15s.

---

## How do I contribute?

**Read [CONTRIBUTING.md](CONTRIBUTING.md), branch, change, test, open a PR — and tell us what hardware you tested on.**

The single most valuable thing in a PR is a line like *"verified on a Dell R740 with iDRAC 9"*, because most contributors cannot borrow your server.

Three things to know before starting:

1. **stdlib-only is a feature, not an accident.** A contribution adding a dependency needs an issue explaining why nothing in the standard library works.
2. **The envelope is the contract.** Changing the JSON shape breaks every downstream system anyone has built. Open an issue first.
3. **This repo is a published snapshot.** Your PR lands here and is reviewed for inclusion upstream when a snapshot is cut, at minimum quarterly.

Full guidelines, including what is and is not in scope, are in [CONTRIBUTING.md](CONTRIBUTING.md).

---

## Frequently asked questions

### What is NodX?

**NodX is a datacenter infrastructure monitoring platform** for hardware telemetry — power, temperature, cooling — across a distributed fleet. This repository is the open-source host agent that collects those readings.

### Is this agent free to use?

**Yes.** It is licensed under the [Apache License 2.0](LICENSE), which permits use, modification, distribution, and private deployment, including commercial use.

### Can I fork it and run my own backend?

**Yes, that is an intended use case.** The protocol is one POST endpoint and six JSON fields; the [reference receiver](#how-do-i-send-telemetry-to-my-own-backend) above is a complete working example. Fork it, change it, and run it in your own stack.

### Does it send data to NodX?

**No, not unless you point it there.** The agent posts only to the URL in `NODX_SERVER`. In `--stdout` mode it makes no network connections at all.

### Does it work in Docker or Kubernetes?

**It works, with caveats.** Inside a container the agent reads the *container's* `/proc` and `/sys`, so thermal, hwmon, and IPMI readings reflect the container's namespace rather than the host. For genuine hardware telemetry, run it on the host, or mount `/proc` and `/sys` from the host. For Kubernetes, a `DaemonSet` with host mounts is the usual approach.

### Why is there no Prometheus exporter built in?

**Scope.** Pull-based exposition is a different delivery model with its own failure modes, and adding it would double the surface of a deliberately small agent. [node_exporter](https://github.com/prometheus/node_exporter) already does that job excellently — the [comparison table](#how-does-it-compare-to-prometheus-node_exporter) will help you choose.

### Why does it refuse to start without configuration?

**Because a silently misconfigured telemetry agent is worse than a stopped one.** Default credentials, auto-generated identity, or a fallback endpoint would each produce an agent that appears healthy while delivering nothing — or delivering your metrics to an unintended destination. It fails fast instead.

### What is the difference between `agent_id` and `device_id`?

`agent_id` is the **claimed identity of the agent installation** — a UUID assigned by a human, persisted at first boot, and never self-generated. `device_id` is **derived from it** deterministically (UUIDv5 over `agent_id` + `"self"`), so one agent maps to exactly one device.

### How much CPU and memory does it use?

Modest: one goroutine polls on an interval of 15s by default, subprocess readers are killed after 10s, and there is no allocation-heavy path between reads. The collection loop is synchronous on purpose — spawning a goroutine per tick would allow overlapping generations under stall. Precise figures for your hardware are best obtained by running it under `time` and reading `/debug/vars`.

### How do I change the collection interval?

Set `poll_interval` in YAML or `NODX_POLL_INTERVAL` in the environment. The agent applies ±10% jitter automatically so a fleet does not synchronise.

### Where can I report a security vulnerability?

Privately, through [GitHub Security Advisories](https://github.com/nodx-ops/nodx-agent/security/advisories/new) — please do not open a public issue. The agent handles credentials and runs with hardware access.

### What metrics does NodX collect?

Temperature, power draw, fan speed, BMC sensors, GPU metrics, CPU load, and memory — see [What does this agent collect?](#what-does-this-agent-collect) for the full list with sources.

---

## Project status

This agent is **production software** used to collect telemetry, and simultaneously a **young open-source project** — the repository, docs, and contributor tooling are being built out. Expect rough edges in documentation before you find them in the collector.

- **License:** [Apache 2.0](LICENSE)
- **Platform:** [nodx-ops.com](https://nodx-ops.com)
- **Issues:** [github.com/nodx-ops/nodx-agent/issues](https://github.com/nodx-ops/nodx-agent/issues)
- **Contributing:** [CONTRIBUTING.md](CONTRIBUTING.md)

```sh
go build ./cmd/nodx-agent && ./nodx-agent --stdout
```
