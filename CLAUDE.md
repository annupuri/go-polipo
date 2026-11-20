# go-polipo

A Go rewrite of Polipo, the caching web proxy, forked from Polipo 1.1.1
(`jech/polipo`). The original's C sources are gone; this repository is Go only.
In comparisons, "v1.1.x" means the original C Polipo and "v2.0.x" this rewrite
(the binary is still called `polipo`).

## Commands

| Command | What it does |
|---|---|
| `make build` | Build `bin/polipo` (`make build-windows` for `bin/polipo.exe`) |
| `make test` / `go test ./...` | Run the tests. CI also runs them with `-race` |
| `make vet`, `gofmt -l .` | Must be clean; CI fails on either |
| `make vulncheck` | govulncheck (CI runs it too) |
| `make lint` | golangci-lint with `.golangci.yml`; must be clean, CI runs it too |
| `make cover` | Tests with a coverage summary |

Run gofmt, vet, `make lint` and the tests before every commit.

## Principles

- **Match v1.1.x behaviour and defaults** (`allowedPorts`, `tunnelAllowedPorts`,
  freshness heuristics, ...) unless RFC compliance or security requires
  otherwise, and record any deliberate difference in `docs/PARITY_AUDIT.md`.
  Do not widen defaults for the sake of one site; that is a per-user
  configuration choice.
- go-polipo caches only `http://` traffic. HTTPS goes through CONNECT tunnels
  and is never inspected or cached.
- PARITY_AUDIT work goes one item at a time: for each item, show a
  v1.1.x / v2.0.x-before / v2.0.x-after comparison and get the maintainer's OK
  before starting the next one.

## Keeping files in step

- **Docs are bilingual.** `README.md`, `docs/INSTALL.md`, `docs/ARCHITECTURE.md`,
  `docs/PARITY_AUDIT.md` and `docs/TEST_VIEWPOINTS.md` each have a `.ja.md`
  twin; change both together.
- **Go version bumps** touch, together: `go.mod`, `.tool-versions`, the asdf
  fallback path in `Makefile`, and the "Go 1.x.y" mentions in `README*.md`,
  `docs/INSTALL*.md` and `CHANGES`. CI reads the version from `go.mod`.
  Afterwards run `asdf reshim golang`, or gopls and other shimmed tools fail.
- **The golangci-lint version** is pinned twice: the `lint` target in
  `Makefile` and the golangci-lint job in `.github/workflows/ci.yml`.
  Change both together.
- **`CONTRIBUTING.md`** repeats the principles, test rules and commit rules
  in this file for outside contributors; when one of them changes here,
  change it there too.
- **CHANGES**: a released entry (one with a dateline) is final. User-visible
  changes since the last release go in a topmost "Unreleased: go-polipo
  X.Y.Z" entry; if the topmost entry is already released, start one with
  the next version, and fold later work into it instead of adding entries.
  Refactoring and test-only changes need no entry. The dateline,
  `version.go` (with `windows/polipo.iss`) and the release tag are settled
  when it is released.
- Defaults appear in `config.go`, `config.sample`, the README tables and
  `docs/PARITY_AUDIT*.md`; keep them consistent.

## Tests

- A test that needs a port nothing listens on must use the `deadLoopback`
  constant (`[::1]`) from `proxy_test.go`, not `127.0.0.1`. Under WSL's
  mirrored networking a connect to a closed `127.0.0.1` port is dropped
  instead of refused, and the test hangs until the go test timeout.
- A bug fix comes with a test that fails without the fix.
- `docs/TEST_VIEWPOINTS.md` lists what the tests should look at and which
  tests do. When adding or changing behaviour, walk the rows it touches and
  add the new tests to them; a bug fix adds the missed case to its row.

## Do not write

- **Names or hostnames of grey-area sites**, in code, comments, tests, docs or
  commit messages. Describe the behaviour generically ("a CONNECT to a
  non-standard port") or use a neutral host such as `example.com`.
- Anything specific to one person's machine: user names, SIDs, local paths,
  private hostnames or IP addresses, log excerpts. Use placeholders such as
  `<you>` and `<YourDistroName>`.
- `tmp/` is a private scratch directory; it is gitignored and never committed.

## Commits

- Messages in English: a short imperative subject, then a body explaining why.
- One purpose per commit. Keep refactoring separate from behaviour changes.
- **Never commit on `main`.** `main` changes only through a pull request.
  Before the first commit of a piece of work, create a topic branch from an
  up-to-date `main`, named after its purpose (`fix/...`, `feat/...`,
  `docs/...`, `refactor/...`). If you find yourself on `main` with
  uncommitted changes, move them to a new branch before committing.
- Pull requests are merged with "Rebase and merge", so every commit on the
  branch lands on `main` as is. Keep the branch's commits clean.
- **The maintainer pushes and merges.** Claude stops at commit on the topic
  branch and never runs `git push`, `gh pr create`, `gh pr merge` or anything
  else that changes `origin`.
- **Do not check out or run an outside contributor's unmerged branch** (build,
  test, `make`, or start a session in it) until the maintainer has reviewed
  its diff, especially changes to `CLAUDE.md`, `.claude/`, `.mcp.json`,
  `Makefile`, `.github/` and test code. Review such a pull request from its
  diff only.
