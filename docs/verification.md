# Verification gates

The Go verification workflow runs on pull requests, pushes to `main`, and manual dispatch. It uses read-only repository permissions, commit-pinned actions, and no persisted checkout credentials.

## Executed checks

- Linux and macOS: regular and `integration` profiles with race detection and shuffled test order.
- Both platforms: a five-second NTS-KE parser fuzz smoke test. This is bounded smoke coverage, not exhaustive fuzzing.
- Linux: gofmt, `go mod tidy` with clean-tree verification, vet in both profiles, reachable vulnerability scanning, and baseline-aware govet/ineffassign lint.

Go is read from `go.mod`. Tool versions are pinned in the workflow; update them deliberately. Module caching is disabled because this zero-dependency repository does not contain `go.sum`.

## Lint baseline

Lint reports new findings relative to `5b4826e5c712e77f947d73e69f24b940ac3ad4f9`, the pre-fix `main` revision. Three pre-existing ineffectual assignments (one in `clock/public.go`, two in `internal/siv/aes_gcm_siv.go`) reproduce on that exact revision. The baseline is frozen rather than moved forward on each PR, so subsequently introduced findings remain visible. This gate does not claim those older warnings have been fixed, and is not a full linter suite.

## Limits and merge order

The integration tests use loopback TLS/UDP peers and simulated clocks, not public NTP/NTS servers or actual VM/suspend events. Passing tests do not prove hardware clock guarantees. Vulnerability results depend on the database available when the job runs.

This workflow PR is stacked on the assurance/NTS fixes in PR #7 because it invokes the new `FuzzReadServerResponse` target. Merge #7 first, then retarget this PR to `main`. No branch-protection changes are included; maintainers may require these checks separately.
