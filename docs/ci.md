# Continuous Integration

## Purpose

TraceBudget uses continuous integration to validate every change to the main branch and every pull request targeting it. The workflow reports whether the repository passes the same quality gates required for local development. It does not deploy or publish artifacts.

## Triggering events

The workflow runs for:

- pushes to `main`;
- pull requests targeting `main`.

## Execution environment

The workflow runs as one sequential job on the latest GitHub-hosted Ubuntu runner. It checks out the repository with credentials disabled after checkout and grants only read access to repository contents.

Go is installed from the version declared in `go.mod`. Module and build caching use `go.sum` so dependency changes invalidate the relevant cache.

## Quality gates

The job runs these commands in order:

```sh
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

Any nonzero exit stops the job and marks the workflow run as failed. A successful run means every command completed successfully on the checked-out revision.

## Scope

The workflow does not:

- deploy the application;
- publish releases or artifacts;
- use repository secrets;
- change application behavior;
- add a platform or Go-version test matrix.

Additional platforms, release automation, or security scanning should be added only when a concrete project requirement justifies them.

## Acceptance criteria

- The workflow is valid GitHub Actions YAML.
- It runs on pushes to `main` and pull requests targeting `main`.
- It reads the Go version from `go.mod`.
- It runs all four quality gates.
- It has read-only repository permissions.
- The local quality gates pass before the workflow is committed.
