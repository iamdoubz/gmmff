---
type: Documentation
title: Testing Guidance
description: How to run tests, understand test coverage, and contribute tests for gmmff.
verified:
  - by: openwiki/0.5.0
    at: 2026-09-05T11:33:48.919Z
sources:
  - id: openwiki-source-a2371d6362e5db4bc834ad03
    resource: repo://CLAUDE.md
  - id: openwiki-source-f3afda225ef2a83eb7d696c1
    resource: repo://docs/TEST-PLAN.md
  - id: openwiki-source-012f2c78e3b1446dfc35803f
    resource: repo://Makefile
generated: { by: "openwiki/0.5.0", at: "2026-09-05T11:33:48.919Z" }
---
# Testing Guidance

## Running Tests

### Unit Tests
```bash
make test
```
Runs all tests (CGO-disabled, works on Windows). This is the default test command.

### Race Detection
```bash
make test-race
```
Runs tests with race detector enabled. Requires clang and a non-Windows host.
Does not work on Windows (MSVC `-mthreads` error) — use `make test` there.

### Coverage
```bash
make test-cover
```
Runs tests and generates a coverage profile, then prints functional coverage.

```bash
make cover
```
Runs `make test-cover` and then opens an HTML coverage report in the browser.

### Specific Packages
```bash
go test ./internal/slot/...   # Test only slot package
go test ./internal/transfer/  # Test transfer package
```

## Test Structure

### Test Philosophy
Tests here have caught **real production bugs**, not hypothetical ones:
- Schedule auth bypass (empty IP list short-circuited the password check)
- Path traversal (`sanitiseName` left `..` in `../../x`)
- Non-deterministic byte-size parsing (`"gb"` matching as `"b"`)
- `formatDurationLabel(0)` edge case

Because of that track record, tests are treated as a first-class safety net, not
a box to tick. Two working rules:

1. **When a test fails, decide whether the test or the code is wrong.** Both
   happen. The schedule max-downloads cap is a *ceiling*, not a floor — a test
   once asserted the wrong direction and the test was the bug.
2. **Security-relevant tests are load-bearing.** The PAKE cross-key rejection,
   offer≠answer MAC separation, `sanitiseName` traversal stripping, schedule
   auth precedence, and wire-tag pinning all encode security invariants.
   Changing them should require deliberate justification.

### Test Tiers
<!-- openwiki: broken internal link [docs/TEST-PLAN.md] file "docs/TEST-PLAN.md" does not exist. Fix the href or restore the target, then delete this comment. -->
The project follows a tiered testing approach documented in [TEST-PLAN.md](docs/TEST-PLAN.md):

- **Completed tiers (1–8d)**: Unit and integration tests covering core packages
<!-- openwiki: broken internal link [docs/TEST-PLAN.md] file "docs/TEST-PLAN.md" does not exist. Fix the href or restore the target, then delete this comment. -->
  (see [TEST-PLAN.md](docs/TEST-PLAN.md) for detailed coverage by package).
- **Pending tier (8e)**: Integration tests with real Redis and session/WebRTC
  integration.

### Test Organization
- Unit tests live alongside the code they test (`*_test.go`)
- Table-driven tests are preferred for pure logic
- Mocks are used for external dependencies (e.g., `mockDataChannel` for WebRTC)
- Integration tests use `httptest` for HTTP handlers and `miniredis` for Redis
  integration where applicable

## Writing Tests

### Principles
1. **Test real behavior, not mocks** where possible
2. **Security-relevant tests are load-bearing** - do not modify without strong justification
3. **When a test fails, determine if the test or code is wrong** - both happen
4. **Focus on boundaries and invariants** - test state machines, validation, error paths

### Common Patterns
- Use `require.NoError(t, err)` for assertions (from `github.com/stretchr/testify/require`)
- Table-driven tests for functions with multiple input/output cases
- Mock implementations for interfaces (see `internal/transfer/mockDataChannel.go`)
- Golden file testing for complex output (see `internal/display`)

### Security Tests
Particular attention is paid to:
- PAKE cross-key rejection
- SDP offer≠answer MAC separation
- Input sanitization (path traversal, byte-size parsing)
- Authentication precedence
- Wire-tag pinning

These tests should not be changed without deliberate justification and preferably
accompanied by a security review.

## Continuous Integration

GitHub Actions runs:
- `make test` on every push and pull request
- `make test-race` on weekly schedule
- Security scanning workflows (see `.github/workflows/vuln.yml`)
- Docker build and push

## Benchmarks

Benchmarks are located alongside tests in `*_test.go` files and follow the naming
convention `Benchmark*`.

Run benchmarks:
```bash
go test ./... -bench=.
```

Run with allocation profiling:
```bash
go test ./... -bench=. -benchmem
```
