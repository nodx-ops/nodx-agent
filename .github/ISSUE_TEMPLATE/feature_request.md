---
name: Feature request
about: A new sensor, platform, or capability for the agent
title: ""
labels: enhancement
assignees: ""
---

## What would you like?

Describe the capability. If it is a metric, give the exact name you would
expect to see in the envelope.

## Why would this matter?

What decision or alert does this enable? What are you unable to do today?

## Hardware / platform involved

- Server or device:
- Sensor source (sysfs path, IPMI sensor, GPU tool, Redfish property):

If you can, paste raw readings:

```sh
ipmitool -H <bmc> -U <user> -P <pass> sdr list | head -30
cat /sys/class/thermal/thermal_zone*/temp
```

## Proposed shape

- [ ] New reader in `internal/collect/`
- [ ] New metric name (please suggest one)
- [ ] No new dependency required (preferred)

## Would you be willing to implement it?

Yes / No / With guidance

> A note on scope: this agent deliberately does **not** call backend
> endpoints beyond telemetry delivery. Features requiring new server-side
> behaviour belong upstream in the platform, not here. If you are unsure
> whether your idea fits, open the issue and ask — we will tell you plainly.
