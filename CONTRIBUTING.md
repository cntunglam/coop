# Contributing to coop

Thanks for your interest in improving **coop**! Contributions of all kinds —
bug reports, docs, features, and fixes — are welcome.

## Getting started

```sh
git clone https://github.com/cntunglam/coop.git
cd coop
go build -o coop ./cmd/coop   # build
go test ./...                 # run the suite
```

Requires **Go 1.25+** on macOS or Linux. Some tests spawn real child processes
(`sleep`, `python3 -m http.server`) and drive the TUI headlessly via a
simulation screen, so those binaries need to be on your `PATH`.

## Making a change

1. **Fork** the repo and create a branch off `main` (`git switch -c my-change`).
2. Make your change with a focused scope — small PRs are easier to review.
3. Before pushing, make sure it's clean:
   ```sh
   gofmt -w internal cmd     # formatting is enforced in CI
   go vet ./...
   go test ./...
   ```
4. Push your branch and open a **pull request against `main`**.
5. CI (build, `gofmt`, `go vet`, `go test -race`) must pass before a PR can merge.

## Conventions

- **`gofmt` is the source of truth** for formatting; CI fails on unformatted code.
- Match the surrounding code's style, naming, and comment density.
- Add or update tests for behavior changes. The daemon lifecycle and TUI
  suspend/signal paths have subprocess / simulation-screen test patterns
  (`TestSuspendSignalIsolation`, `TestStartAndStopDaemonViaKeys`) — prefer those.
- Keep the design as-is where it's deliberately narrowed (e.g. presets only, no
  per-run override flags). If you want to change a design decision, open an issue
  first so we can discuss it.

## Reporting bugs & requesting features

Open an [issue](https://github.com/cntunglam/coop/issues). For bugs, include your
OS, `coop` version (`coop --version`), the exact command, and what happened vs.
what you expected. If the TUI wedges, a `SIGUSR1` goroutine dump
(`kill -USR1 $(pgrep coop)` → `~/.local/state/coop/goroutines.txt`) is gold.

## License

By contributing, you agree that your contributions are licensed under the
project's [MIT License](LICENSE).
