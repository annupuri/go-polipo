Contributing to go-polipo
=========================

Thank you for your interest in go-polipo. This file explains what kind of
change fits the project and how to get one merged.

Scope
-----

go-polipo is a Go rewrite of Polipo 1.1.1, the caching web proxy. A few
principles decide whether a change is accepted:

- **Polipo 1.1.x behaviour and defaults are the reference** (`allowedPorts`,
  `tunnelAllowedPorts`, freshness heuristics, ...). A change departs from them
  only when RFC compliance or security requires it, and every deliberate
  difference is recorded in `docs/PARITY_AUDIT.md`.
- **Defaults are not widened for the sake of one site.** If a site needs, for
  example, an extra tunnel port, that is a per-user configuration choice.
- **Only `http://` traffic is cached.** HTTPS goes through CONNECT tunnels and
  is never inspected or cached. Changes that would decrypt or cache HTTPS are
  out of scope.

For anything larger than a small fix, please open an issue first so we can
agree on the approach before you spend time on it.

Security issues
---------------

Please do not report security problems in a public issue. Use the
**Report a vulnerability** button on the repository's Security tab instead.

Workflow
--------

1. Fork the repository and create a topic branch from an up-to-date `main`
   (for example `fix/vary-header` or `docs/install-wsl`).
2. Make your change, with tests (see below).
3. Run the checks locally:

       gofmt -l .        # must print nothing
       make vet
       make test         # CI also runs the tests with -race
       make vulncheck

   The Go version is the one in `go.mod`.
4. Open a pull request against `main`. CI (build / vet / test and
   govulncheck) must pass before the pull request can be merged.

`main` is protected: every change, including the maintainer's own, goes in
through a pull request. Pull requests are merged with "Rebase and merge", so
each of your commits lands on `main` as it is. Please keep them clean; you may
be asked to squash fix-up commits before merging.

Pull requests that touch `CLAUDE.md`, `.claude/`, `.mcp.json`, `Makefile`,
`.github/` or the build and test setup get extra scrutiny, because those files
can run code on the maintainer's machine or in CI. Please explain why such a
change is needed in the pull request description.

Tests
-----

- A bug fix comes with a test that fails without the fix.
- A test that needs a port nothing listens on must use the `deadLoopback`
  constant (`[::1]`) from `proxy_test.go`, not `127.0.0.1`. Under WSL's
  mirrored networking a connect to a closed `127.0.0.1` port is dropped
  instead of refused, and the test hangs until the `go test` timeout.
- `docs/TEST_VIEWPOINTS.md` lists what the tests should look at and which
  tests do. When you add or change behaviour, walk the rows it touches and
  add your tests to them; a bug fix adds the missed case to its row.

Keeping files in step
---------------------

- **Defaults** appear in `config.go`, `config.sample`, the README tables and
  `docs/PARITY_AUDIT*.md`. A change to a default updates all of them.
- **Docs are bilingual.** `README.md`, `docs/INSTALL.md`,
  `docs/ARCHITECTURE.md`, `docs/PARITY_AUDIT.md` and `docs/TEST_VIEWPOINTS.md`
  each have a `.ja.md`
  (Japanese) twin. If you cannot write Japanese, update the English file only
  and say so in the pull request; the maintainer will update the twin.
- **CHANGES**: describe user-visible changes in the topmost entry.

Commits
-------

- Write messages in English: a short imperative subject ("Fix ...",
  "Add ..."), a blank line, then a body explaining *why*.
- One purpose per commit. Keep refactoring separate from behaviour changes.
- Do not name the hosts of grey-area sites (piracy, adult content, ...) in
  code, comments, tests, docs or commit messages. Describe the behaviour
  generically ("a CONNECT to a non-standard port") or use a neutral host such
  as `example.com`.
- Do not include anything specific to your machine: user names, local paths,
  private hostnames or IP addresses, or unedited log excerpts. Use
  placeholders such as `<you>`.

License
-------

go-polipo is distributed under the MIT license (see `LICENSE`). By submitting
a pull request you agree that your contribution is licensed under the same
terms.
