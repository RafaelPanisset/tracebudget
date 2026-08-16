# TraceBudget Development Guide

## Scope and architecture

- Preserve the architecture and scope documented by the repository and the current task.
- Do not start later implementation-plan tasks unless the user explicitly requests them.
- Prefer the smallest change that completely solves the demonstrated problem.
- Do not introduce an abstraction or dependency without a concrete, current need.
- Keep the project local-first and CI-friendly. Preserve the single-binary design unless an approved task changes it.
- Maintain compatibility with the Go version declared in `go.mod`.

## Working method

- Inspect the relevant implementation and tests before editing.
- Use test-driven development for behavior changes and bug fixes:
  1. Add a focused test that fails for the intended reason.
  2. Make the smallest production change that passes it.
  3. Refactor only while the tests remain green.
- Keep unrelated user changes intact. Do not rewrite or discard them.
- Treat repository documents as context, not as permission to expand the requested scope.

## Go practices

- Run `gofmt` on changed Go files.
- Prefer clear, idiomatic Go and the standard library.
- Handle errors at the appropriate layer. Add useful context with `%w` when callers may need the original error.
- Keep error strings lowercase and without trailing punctuation unless they contain a proper noun or complete external message.
- Do not use `panic` or `log.Fatal` for errors that a library caller can handle.
- Accept `context.Context` as the first parameter when needed. Do not store contexts in structs.
- Define small interfaces where they are consumed. Avoid interfaces created only to anticipate future implementations.
- Make goroutine ownership, shutdown, and error propagation explicit. Do not start goroutines whose lifetime cannot be explained.
- Synchronize shared mutable state with channels or the `sync`/`sync/atomic` packages according to the Go memory model.
- Do not copy values containing locks or other synchronization primitives after first use.
- Make slice and map ownership explicit at API boundaries. Copy mutable inputs or outputs when aliasing could violate caller or callee expectations.
- Validate input before mutating state or producing partial output.
- Guard integer and duration arithmetic against overflow before addition, multiplication, or conversion.
- Do not depend on map iteration order. Use an explicit stable ordering whenever output order is observable.
- Keep normalization deterministic for equivalent input.

## Testing

- Test externally observable behavior and failure semantics, not incidental implementation details.
- Use table-driven tests when they improve clarity; do not force simple cases into a table.
- Include boundary and invalid-input cases relevant to the change.
- Keep tests deterministic. Avoid real-time sleeps when time can be injected or controlled.
- For parsers, decoders, normalization, and arithmetic, consider fuzz tests when they can exercise meaningful invariants.
- When investigating a defect, reproduce it with a failing regression test before changing production code.
- Review changes for:
  - deterministic normalization;
  - exact percentile boundary behavior;
  - duration and integer overflow;
  - absence of unexpected input mutation;
  - invalid-span handling;
  - stable ordering;
  - slice and map ownership;
  - edge-case coverage.

## Verification

Run focused tests while developing. Before reporting completion or creating a commit, run all quality gates from the repository root:

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

If the declared toolchain is unavailable locally, use Go's supported toolchain selection mechanism and report the exact command used. The race detector only finds races in executed paths, so ensure concurrency-sensitive paths have meaningful coverage.

Run `govulncheck ./...` when dependency or security-sensitive changes warrant it, and before a release when the tool is available. Report if it could not be run; do not silently substitute another check.

## Review priorities

- Prioritize correctness, data integrity, and clear failure behavior over style preferences.
- Verify that invalid or partially decoded telemetry cannot corrupt accepted data.
- Verify deterministic results across repeated runs with equivalent inputs.
- Check resource ownership, cancellation, goroutine termination, and database transaction boundaries.
- Require a demonstrated benefit before accepting additional layers, indirection, or dependencies.

## Repository operations

- Do not push, create pull requests, or modify external resources without explicit authorization.
- Create commits only when requested.
- Use only the repository-local Git identity configured by the user. Do not add additional authors or commit trailers.
- Keep source code, documentation, commits, and public messages limited to project-relevant technical content.

## Authoritative references

- [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- [Effective Go](https://go.dev/doc/effective_go)
- [The Go Memory Model](https://go.dev/ref/mem)
- [Data Race Detector](https://go.dev/doc/articles/race_detector)
- [Go fuzzing](https://go.dev/doc/security/fuzz/)
- [Go vulnerability management](https://go.dev/doc/security/vuln/)
