---
name: Bug report
about: A reader misreads hardware, the agent crashes, or output looks wrong
title: ""
labels: bug
assignees: ""
---

## Describe the bug

A clear description of what went wrong.

## Hardware (most important field)

- Server / motherboard:
- BMC type and firmware (iDRAC / iLO / IPMI / Redfish):
- CPU vendor:
- OS / kernel:

Without this we cannot reproduce it — most maintainers will not have your
machine.

## To reproduce

```sh
./nodx-agent --stdout 2>stderr.log
```

Paste the relevant envelopes **and** the stderr log:

```
(paste stdout envelopes here)
```

```
(paste stderr.log here)
```

## Expected behaviour

What you expected to see instead.

## Environment

- Agent version (the release tag you built from, or `agent_version` from
  `curl -s http://127.0.0.1:6060/debug/vars | jq -r .agent_version`):
- OS / kernel:
- Install method (`.deb` / `.rpm` / Docker / built from source):

> There is no `--version` flag — `agent_version` is reported via the
> loopback expvar endpoint instead.

## Additional context

Anything else — unusual kernel config, containers, read-only `/sys`,
virtualization.
