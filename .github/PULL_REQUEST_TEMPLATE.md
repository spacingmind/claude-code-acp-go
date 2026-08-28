## What changed and why

<!-- Describe the change and the motivation behind it. -->

## Checklist

- [ ] `go build ./...`, `go test -race ./...`, `go vet ./...`, `gofmt -l .`,
      and `golangci-lint run` all pass locally.
- [ ] PR title follows [Conventional Commits](https://www.conventionalcommits.org/)
      (`feat:`, `fix:`, `feat!:`/`BREAKING CHANGE:`, `chore:`, `docs:`,
      `ci:`, `test:`) — required since this PR will be squash-merged into
      `develop`'s history and, eventually, `main`'s release-please
      changelog.
- [ ] This PR targets `develop`, not `main`.
- [ ] For nontrivial changes: a plan doc under `docs/plans/` was created or
      updated (`docs/plans/active/<slug>.md`, per `AGENTS.md` rule (c)) —
      or this change is small/bounded enough not to need one.
