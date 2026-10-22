## What does this change?

<!-- One or two sentences. Imperative mood: "fix(ipmi): tolerate missing fans" -->

## Hardware tested on

<!-- This matters more than the diff. Which machine, which BMC, which CPU? -->

- Machine / motherboard:
- BMC + firmware:
- OS / kernel:

## How was it verified?

<!-- e.g. "--stdout output before/after", "go test ./...", "ran 24h on a Dell R740" -->

```sh
# paste commands and notable output
```

## Checklist

- [ ] `gofmt -l .` prints nothing
- [ ] `go vet ./...` passes
- [ ] `go test ./...` passes
- [ ] No new dependency added to `go.mod` (stdlib-only — see CONTRIBUTING)
- [ ] Envelope/JSON contract unchanged, **or** an issue was opened first
- [ ] Internal task IDs / private URLs are not referenced in code or commits

## Notes for reviewers

<!-- Anything risky, anything you deliberately did not do, anything you want a second pair of eyes on -->
