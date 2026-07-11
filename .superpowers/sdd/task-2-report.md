# Task 2 Report: `internal/subscription/store.go` + Tests

## What I Implemented

- **`internal/subscription/store.go`** — Subscription persistence with `NamedSubscription` struct, `LoadSubscriptions()` (reads `subscriptions.txt` with `# comment` support and hostname-based name fallback), `SaveSubscription()` (appends deduplicated URLs).
- **`internal/subscription/store_test.go`** — 4 tests covering: missing file, save+load round-trip, comments as names, and hostname fallback when no comment.

## TDD Evidence

### RED phase — tests fail (compilation error, symbols undefined)

```
> go test ./internal/subscription/ -run TestLoad -v
# xray-runner/internal/subscription [xray-runner/internal/subscription.test]
internal\subscription\store_test.go:10:10: undefined: subscriptionsFile
internal\subscription\store_test.go:11:2: undefined: subscriptionsFile
...
FAIL    xray-runner/internal/subscription [build failed]
```

### GREEN phase — tests pass

```
> go test ./internal/subscription/ -run TestLoad -v
=== RUN   TestLoadSubscriptions_FileNotExist
--- PASS: TestLoadSubscriptions_FileNotExist (0.00s)
=== RUN   TestLoadSubscriptions_WithComments
--- PASS: TestLoadSubscriptions_WithComments (0.00s)
=== RUN   TestLoadSubscriptions_WithoutComments
--- PASS: TestLoadSubscriptions_WithoutComments (0.00s)
PASS
ok  	xray-runner/internal/subscription	0.818s

> go test ./internal/subscription/ -run TestSaveAndLoadSubscription -v
=== RUN   TestSaveAndLoadSubscription
--- PASS: TestSaveAndLoadSubscription (0.00s)
```

## Full Test Results

All 26 tests in `internal/subscription` pass (including 4 new ones). No regressions.

```
> go test ./internal/subscription/ -v
... 26 tests, all PASS ...
PASS
ok      xray-runner/internal/subscription   1.133s
```

## Build

`go build ./...` — exits with no output (success).

## Files Changed

2 new files:
- `internal/subscription/store.go` (75 lines)
- `internal/subscription/store_test.go` (98 lines)

## Self-Review Findings

- Implementation matches the brief verbatim — zero deviations.
- `SaveSubscription` reads existing subs to deduplicate (as specified).
- Missing file returns `nil, nil` (not an error) per spec.
- `LoadSubscriptions` respects `# comment` → `Name`, falls back to URL hostname.
- No unused imports or orphaned code.

## Concerns

None.
