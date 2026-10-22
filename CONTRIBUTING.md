# Contributing to NodX Agent

Thanks for your interest in contributing. This document is short on purpose:
the bar for a useful contribution is a working change, not paperwork.

## The one thing to know about this repository

**This repository is a published snapshot of the agent's canonical source.**

- Canonical lives in a separate private repository.
- Direction **in** is one-way: canonical → this repository, as clean sync
  commits (`sync: agent snapshot @ <version>`). Internal history is never
  replayed here.
- Direction **out** (your contributions) is **human-reviewed**, never automated.

### Where your PR goes

Your pull request lands **here**. It does not automatically reach production
or the canonical tree.

**Review cadence for cherry-picking your changes into canonical:**

> Reviewed **when an agent snapshot is cut**, and **at minimum quarterly**.

If that cadence ever slips, call it out in an issue — silence is the thing
that kills open-source projects, and we would rather be told.

We will say plainly when a change cannot be accepted (for example, it would
require a backend feature this agent cannot assume). A clear "not this one,
here's why" is a real answer; an ignored PR is not.

## Getting started

```sh
git clone https://github.com/nodx-ops/nodx-agent.git
cd nodx-agent
go test ./...
go build ./cmd/nodx-agent
```

Requirements: **Go 1.26+**. No other dependencies — the agent is stdlib-only
by design, and we intend to keep it that way.

### See it work before you change anything

```sh
./nodx-agent --stdout
```

This collects from your own machine and prints one JSON envelope per line.
No server, no account, no config file:

```json
{"device_id":"7b89d024-78b8-5ffd-80ae-6d6f18947477","metric":"cpu_load_1m","value":2.07,"timestamp":1792646719,"agent_id":"00000000-0000-5000-8000-000000000000","protocol":"agent"}
```

Logs go to **stderr**, envelopes to **stdout**, so you can pipe it:

```sh
./nodx-agent --stdout 2>/dev/null | jq -r '"\(.metric)=\(.value)"'
```

Expect **partial emission**: sources your host does not have (IPMI without
`ipmitool`, GPU without `nvidia-smi`) are skipped and reported on stderr.
That is intended behaviour, not a bug.

## What makes a good contribution

| Welcome | Probably not |
|---|---|
| A new hardware sensor or platform reader | A change that requires this agent to call a new backend endpoint |
| Fixing a reader that misreads real hardware | Anything adding a third-party dependency |
| Clearer docs, examples, correction of an error | Reformatting unrelated code |
| Tests for untested paths | Silent changes to the envelope contract |
| Build/packaging improvements | Large refactors without a preceding issue |

### The envelope is the contract

The JSON shape in [`internal/collect/envelope.go`](internal/collect/envelope.go)
is what third-party backends consume. **Changing it breaks every downstream
system anyone has built.** If your change must alter it, open an issue first —
do not surprise people in a PR.

### The stdlib-only rule

The agent runs on datacenter hosts, sometimes air-gapped, sometimes where
supply chain review is a condition of purchase. Zero external modules is a
feature. `go.mod` should stay empty of `require` blocks; a contribution that
adds one needs an issue explaining why nothing in the standard library works.

## Style

- `gofmt` clean (`gofmt -l .` prints nothing) — CI enforces it.
- `go vet ./...` clean.
- Comments explain **why**, especially for anything that looks defensive.
  This codebase assumes a hostile, failure-prone environment on purpose.
- Never `log.Fatal` from a library path; the command decides process exit.

## Pull requests

1. Fork and create a branch: `git checkout -b fix/sensor-name`
2. Make the change, add or update tests
3. Ensure `go test ./...` passes
4. Open a PR with a short description of **what hardware you tested on**

**Please tell us the hardware.** "Works on an iDRAC 9 / Supermicro X11" is
worth more than any amount of code review, because most contributors cannot
run your equipment.

### Commit messages

Imperative, specific, no task IDs from any internal tracker:

```
fix(ipmi): tolerate incomplete fan readings on iDRAC 9
```

## Reporting bugs

Use the **Bug report** template. The single most useful thing you can include
is the output of:

```sh
./nodx-agent --stdout 2>stderr.log
```

plus your host details (CPU vendor, BMC type, kernel version).

## Security

Do **not** open a public issue for a vulnerability. Report it privately via
GitHub Security Advisories on this repository. The agent handles credentials
and runs with hardware access, so we take that seriously.

## Code of Conduct

By participating you agree to the [Code of Conduct](CODE_OF_CONDUCT.md).

## License

Contributions are licensed under the [Apache License 2.0](LICENSE), the same
as the project. By submitting a contribution you affirm you have the right to
license it under those terms.
