# Contributing to gaal

Thanks for your interest in gaal. Contributions of every size are welcome: bug reports, documentation fixes, new VCS backends, agent definitions, and features.

gaal is a single Go binary that keeps repositories, agent skills, MCP servers, and related files in sync across your AI coding agents. A quick read of [docs/architecture.md](docs/architecture.md) and [AGENTS.md](AGENTS.md) before you start will save you time: they describe the package layout and the conventions the codebase holds itself to.

## Ways to contribute

- **Report a bug or request a feature**: open an [issue](https://github.com/getgaal/gaal/issues/new/choose) using the templates. Questions and ideas are welcome as issues too.
- **Send a change**: open a pull request. You do not need a matching issue first, and small fixes can go straight to a PR. For larger or design-heavy changes, opening an issue up front is appreciated so we can agree on the approach before you invest the time.
- **Report a security vulnerability**: do not open a public issue. See [SECURITY.md](SECURITY.md).

## Development setup

Prerequisites: **Go 1.26+** and **git**. No network access or external VCS binaries are needed to build or test; the test suite mocks all external I/O.

```bash
git clone https://github.com/getgaal/gaal.git
cd gaal
make build      # compiles to dist/, regenerates the JSON schema
make test       # runs the unit suite
```

Optionally install the repo git hooks:

```bash
make hooks      # sets core.hooksPath to .githooks/
```

Useful targets:

| Command | What it does |
|---------|--------------|
| `make build` | Compile the binary to `dist/` |
| `make test` | Run unit tests |
| `make test-race` | Unit tests with the race detector (what CI runs) |
| `make lint` | `gofmt` formatting check plus `go vet` |
| `make coverage` | Tests with coverage reports in `report/` |
| `make sandbox` | One-shot sync in an isolated `/tmp` directory, never touching your real `$HOME` |

To watch gaal run against the example config without touching your machine:

```bash
make sandbox
```

## Conventions

These are checked in review. See [AGENTS.md](AGENTS.md) for the full set.

- **English only** for code, comments, identifiers, commit messages, and documentation.
- **A unit test for every new function or behaviour.** Tests live beside the code (`internal/foo/foo_test.go`) and use table-driven cases. They must not require network access or installed VCS binaries; mock external I/O with interfaces, `httptest`, or a temp directory.
- **A `slog.Debug` (or `slog.DebugContext`) call in every new function**, logging structured key/value pairs, never secrets.
- **Cross-platform paths**: use `filepath.Join`, `filepath.IsAbs`, `filepath.ToSlash` and the existing `~` expansion helpers, never string concatenation or hardcoded OS paths.
- **Keep `internal/engine` thin.** It is the single orchestrator, not a home for business logic.
- **Coverage target is 90% or higher** on `internal/...`.

Match the style of the surrounding code. `gofmt` is the arbiter of formatting; please do not fight it.

## Opening a pull request

1. Fork the repo and branch from `main`.
2. Make your change, with tests and docs updated for any user-facing behaviour.
3. Before you push, make sure these pass locally. CI runs the same steps in order (lint, build, test, coverage):
   ```bash
   make lint
   make build
   make test-race
   ```
4. Open the PR and fill in the template. Link any related issue with `Closes #NNN`.
5. A code owner reviews. CI must be green and at least one owner must approve before merge. Please be patient and responsive to feedback.

## License

gaal is licensed under the [GNU AGPL-3.0](LICENSE). By submitting a contribution, you agree that your work is licensed under the same terms.
