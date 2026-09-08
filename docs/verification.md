# Verification gates

The Go verification workflow runs on pull requests, pushes to `main`, and manual dispatch. It uses read-only repository permissions, commit-pinned actions, and no persisted checkout credentials.

## Executed checks

- Linux and macOS: regular and `integration` profiles with race detection and shuffled test order.
- Both platforms: five-second NTS-KE and NTS response parser fuzz smoke tests. These are bounded smoke coverage, not exhaustive fuzzing.
- Linux: gofmt, `go mod tidy` with clean-tree verification, vet in both profiles, reachable vulnerability scanning, and baseline-aware govet/ineffassign lint.

Go is read from `go.mod`. Tool versions are pinned in the workflow; update them deliberately. Module caching is disabled because this zero-dependency repository does not contain `go.sum`.

## Lint baseline

Lint reports new findings relative to `5b4826e5c712e77f947d73e69f24b940ac3ad4f9`, the pre-fix `main` revision. Three pre-existing ineffectual assignments (one in `clock/public.go`, two in `internal/siv/aes_gcm_siv.go`) reproduce on that exact revision. The baseline is frozen rather than moved forward on each PR, so subsequently introduced findings remain visible. This gate does not claim those older warnings have been fixed, and is not a full linter suite.

## Limits and merge order

The integration tests use loopback TLS/UDP peers and simulated clocks, not public NTP/NTS servers or actual VM/suspend events. Passing tests do not prove hardware clock guarantees. Vulnerability results depend on the database available when the job runs.

This workflow PR is stacked on the assurance/NTS fixes in PR #7 because it invokes the new `FuzzReadServerResponse` target. Merge #7 first, then retarget this PR to `main`. No branch-protection changes are included; maintainers may require these checks separately.

## Remediation regression gates

- `remediation_test.go` verifies acquisition scale containment, origin/step rejection, controller continuity and rate learning, immutable source configuration, manual-poll shutdown, and zero allocations in the eight steady-state/ready APIs.
- The network recovery regression rewinds the raw counter below the retired estimate anchor and requires a new real NTP round to recover without extrapolating the old mapping.
- `assurance/watermark_epoch_test.go` covers the negative signed timeline, rejected expired evidence, and isolation of retired epoch readers and values.
- `clock/continuity_test.go` distinguishes reordered concurrent completions from a genuine causally later regression; epoch-changing overlaps never relabel old evidence.
- NTP hard bounds are compared with independent `math/big` rational models. NTS fuzzing uses registered AEADs rather than a dummy authenticator.
- The standalone example requires explicit Config/GSTL1 inputs and only reports assured time after a successful real round. Both success and failure are exercised by local peers.

The 2026-09-08 local verification used Go 1.27.0 on darwin/arm64. Unit, race, shuffled integration, vet, gojgp, and both fuzz targets passed; this does not assert that a remote CI run or hardware suspend/restore test occurred. API migration and before/after hot-path measurements are recorded in `fix-plan.md`.
