# Contributing

Small, focused changes are welcome. Open an issue for a behavior change that affects authentication, tokens, platform support or the public CLI, then describe the concrete expected outcome. Never include credentials, access files or real customer identifiers.

Use Go 1.26+; `go.mod` is the toolchain/dependency contract. Read existing code before adding a dependency. Keep current-user behavior, SSH host-key pinning, role separation, expiry and revocation intact.

Before a pull request:

```sh
gofmt -w <changed-go-files>
go vet ./...
go test ./...
go test -race ./...
go test -count=10 ./...
go build ./cmd/shellpier
python3 scripts/validate_skills.py
```

Run relevant platform checks for a platform-specific change. Cross-compilation does not replace a real OS smoke test. Describe tests actually run, failures and remaining gaps. Include a regression test for security/logic fixes; avoid tests that merely duplicate documentation text.

Use clear commit subjects. Conventional Commits (`fix:`, `feat:`, `docs:`) are welcome but not mandatory. Separate unrelated changes. A PR should explain the problem, resulting behavior, security implications and validation; screenshots are optional where useful.

Generated access files, credentials, local Agent rules and internal planning notes must not be committed. Review staged files and `git diff --cached` before committing. Public documentation is part of the product and should match `--help` and executable behavior.

By contributing, you agree your contribution is distributed under the repository's MIT license. Follow [CODE_OF_CONDUCT](CODE_OF_CONDUCT.md). Report vulnerabilities as described in [SECURITY](SECURITY.md).
