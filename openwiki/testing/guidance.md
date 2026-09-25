---
type: Documentation
title: Testing Guidance
description: Comprehensive guide to testing gmmff, covering tiered test strategy, unit/integration/end-to-end tests, chat and scheduling features, Redis vs in-memory store testing, and race detector usage.
tags: [testing, test-strategy, unit-tests, integration-tests, e2e-tests]
verified:
  - by: openwiki/0.6.0
    at: 2026-09-25T13:12:29.362Z
sources:
  - id: openwiki-source-a2371d6362e5db4bc834ad03
    resource: repo://CLAUDE.md
  - id: openwiki-source-f3afda225ef2a83eb7d696c1
    resource: repo://docs/TEST-PLAN.md
  - id: openwiki-source-4b0d190318266514935bd39a
    resource: repo://internal/chat/session_test.go
generated: { by: "openwiki/0.6.0", at: "2026-09-25T13:12:29.362Z" }
---

# Testing Guidance

## Tiered Test Strategy

<!-- openwiki: broken internal link [docs/TEST-PLAN.md] file "docs/TEST-PLAN.md" does not exist. Fix the href or restore the target, then delete this comment. -->
The project follows a tiered testing approach documented in [TEST-PLAN.md](docs/TEST-PLAN.md). Tests are treated as a first-class safety net that has caught real production bugs.

### Completed Tiers (1-8d)
- **Tiers 1-8d**: Completed unit and integration tests covering core packages using mocks and simulated environments
- **Tier 8e (pending)**: Integration tests with real Redis and session/WebRTC integration

### Philosophy
1. **When a test fails, decide whether the test or the code is wrong** - both happen
2. **Security-relevant tests are load-bearing** - PAKE cross-key rejection, offer≠answer MAC separation, `sanitiseName` traversal stripping, schedule auth precedence, and wire-tag pinning all encode security invariants

## Running Tests

### Unit Tests
```bash
make test
```
Runs all unit tests (CGO-disabled, works on Windows). This is the default test command and executes Tiers 1-8d.

### Race Detection
```bash
make test-race
```
Runs tests with race detector enabled. Requires clang and a non-Windows host. From CLAUDE.md: "Does not work on Windows (MSVC `-mthreads` error) — use `make test` there."

### Coverage
```bash
make test-cover
```
Runs tests and generates a coverage profile, then opens the coverage report in your browser.

Alternative:
```bash
go test ./... -coverprofile=coverage.out
go tool cover -html=coverage.out
```

### Specific Packages
```bash
go test ./internal/schedule/...   # Test only scheduling package
go test ./internal/chat/...       # Test only chat package
go test ./internal/store/...      # Test store package
```

## Test Structure

### Test Organization
- Unit tests live alongside the code they test (`*_test.go`)
- Table-driven tests are preferred for pure logic
- Mocks are used for external dependencies (e.g., `mockDataChannel` for WebRTC, miniredis for Redis integration where applicable)
- Integration tests use `httptest` for HTTP handlers

### Testing Chat Feature
Chat functionality is tested through session integration:
- Unit tests for chat frame dispatch and callbacks exist in `internal/chat/session_test.go` (Tier 8a)
- Full chat integration requires live WebRTC data channels and is pending in Tier 8e
- To test chat locally: run two instances with a shared room code and verify message exchange

### Testing Scheduling Feature
Scheduling is covered through multiple test layers:
- Unit tests: `internal/schedule/*_test.go` (config, handler, client, store, cleanup)
- Integration tests: Round-trip tests in `internal/schedule/client_roundtrip_test.go` (Tier 8d) using httptest
- Full scheduling integration with persistent storage is pending in Tier 8e

### Testing with Redis vs In-Memory Store
- **In-Memory Store**: Used for all unit tests (Tiers 1-8d) via `MemStore` implementation in `internal/store`
  - Tests cover full contract suite reusable for Redis integration
  - Run with: `make test`
- **Redis Store Integration**: Pending in Tier 8e
  - Will test against real Redis (or miniredis) to verify TTL expiry, concurrent updates, and code→id index consistency
  - For manual testing: set `GMMFF_REDIS_URL` environment variable and run integration test suite

### Security Tests
Particular attention is paid to:
- PAKE cross-key rejection
- SDP offer≠answer MAC separation
- Input sanitization (path traversal, byte-size parsing)
- Authentication precedence
- Wire-tag pinning
These tests should not be changed without deliberate justification and preferably accompanied by a security review.

## Continuous Integration
GitHub Actions runs:
- `make test` on every push and pull request
- `make test-race` on weekly schedule
- Security scanning workflows (see `.github/workflows/vuln.yml`)
- Docker build and push

## Benchmarks
Benchmarks are located alongside tests in `*_test.go` files and follow the naming convention `Benchmark*`.

Run benchmarks:
```bash
go test ./... -bench=.
```

Run with allocation profiling:
```bash
go test ./... -bench=. -benchmem
```

## Performance Testing
Performance-sensitive areas:
- WebSocket hub performance (concurrent connections) - see `internal/broker/hub_test.go`
- Slot creation/join throughput
- Data channel throughput
- Cryptographic operations (PAKE, HKDF)

## Troubleshooting Tests

### Flaky Tests
If a test fails intermittently:
1. Check for race conditions (use `go test -race`)
2. Verify proper cleanup of resources (especially goroutines, channels, temporary files)
3. Look for dependencies on external state (time, random seeds, global variables)
4. Use `go test -count=1000 .` to reproduce flaky tests locally

### Slow Tests
Tests marked as slow or requiring external resources (Redis, network) should be:
- Tagged appropriately (if using build tags)
- Considered for integration test suite (Tier 8e)
- Run less frequently in local development

### Test Coverage Gaps
<!-- openwiki: broken internal link [docs/TEST-PLAN.md] file "docs/TEST-PLAN.md" does not exist. Fix the href or restore the target, then delete this comment. -->
As of the latest coverage snapshot (see [TEST-PLAN.md](docs/TEST-PLAN.md)), the following packages have low coverage and are targets for improvement:
- `store` (Redis integration needed)
- `chat` (REPL requires live data channel)
- `session` (WebRTC orchestration)
- `peer`, `signaling`, `localmode` (require live WebRTC/WebSocket)

## Resources
<!-- openwiki: broken internal link [docs/TEST-PLAN.md] file "docs/TEST-PLAN.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [TEST-PLAN.md](docs/TEST-PLAN.md) - Detailed test strategy and coverage
<!-- openwiki: broken internal link [docs/DECISIONS.md] file "docs/DECISIONS.md" does not exist. Fix the href or restore the target, then delete this comment. -->
- [docs/DECISIONS.md](docs/DECISIONS.md) - Architectural decisions that may affect testing
- [internal/mocks/] - Mock implementations for testing
- [scripts/] - Helper scripts for development (if any exist)

## Contributing Tests
When adding features:
1. Write unit tests for new functions and methods
2. Test error paths and edge cases
3. For security-sensitive code, add tests that verify the security invariants
4. Update mocks if interfaces change
5. Consider adding integration tests if the feature involves multiple components

Run `make test` before submitting changes to ensure nothing is broken.
