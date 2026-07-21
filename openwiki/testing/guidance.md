---
type: Documentation
title: Testing Guidance
description: How to run tests, understand test coverage, and contribute tests for gmmff.
---
# Testing Guidance

## Running Tests

### Unit Tests
```bash
make test
```
Runs all unit tests (CGO-disabled, works on Windows). This is the default test command.

### Race Detection
```bash
make test-race
```
Runs tests with race detector enabled. Requires clang and a non-Windows host.

### Coverage
## Test Coverage

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
go test ./internal/slot/...   # Test only slot package
go test ./internal/transfer/  # Test transfer package
```

## Test Structure

### Test Tiers
The project follows a tiered testing approach documented in [TEST-PLAN.md](docs/TEST-PLAN.md):

- **Tiers 1-8d**: Completed unit and integration tests covering core packages
- **Tier 8e (pending)**: Integration tests with real Redis and session/WebRTC integration

### Test Organization
- Unit tests live alongside the code they test (`*_test.go`)
- Table-driven tests are preferred for pure logic
- Mocks are used for external dependencies (e.g., `mockDataChannel` for WebRTC)
- Integration tests use `httptest` for HTTP handlers and `miniredis` for Redis integration where applicable

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
- WebSocket hub performance (concurrent connections)
- Slot creation/join throughput
- Data channel throughput
- Cryptographic operations (PAKE, HKDF)

See `internal/broker/hub_test.go` for example concurrent connection tests.

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
As of the latest coverage snapshot (see [TEST-PLAN.md](docs/TEST-PLAN.md)), the following packages have low coverage and are targets for improvement:
- `store` (Redis integration needed)
- `chat` (REPL requires live data channel)
- `session` (WebRTC orchestration)
- `peer`, `signaling`, `localmode` (require live WebRTC/WebSocket)

## Resources

- [TEST-PLAN.md](docs/TEST-PLAN.md) - Detailed test strategy and coverage
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