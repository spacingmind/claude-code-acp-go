# Security Policy

`claude-code-acp-go` is a small, early-stage project without a dedicated
security team. We still take vulnerability reports seriously and will
respond as promptly as we can.

## Reporting a vulnerability

**Please do not open a public GitHub issue for security-sensitive
findings.** Instead, use GitHub's private vulnerability reporting:

<https://github.com/spacingmind/claude-code-acp-go/security/advisories/new>

This opens a private advisory visible only to the maintainers, where you
can describe the issue, its impact, and any reproduction steps. We'll
follow up there.

## Scope

This applies to vulnerabilities in this package itself (the ACP JSON-RPC
layer, session lifecycle handling, permission bridge). Issues in the
underlying [`claude-agent-sdk-go`](https://github.com/spacingmind/claude-agent-sdk-go)
dependency, the `claude` CLI, or Anthropic's services, should be reported
to those projects/Anthropic directly.

## Supported versions

This project is in early development; there is no long-term-support
branch. Security fixes land on the latest release.
