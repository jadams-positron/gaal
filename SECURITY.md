# Security Policy

gaal runs on your machine and writes to your agent configuration files, so we take its security seriously. Thank you for helping keep gaal and its users safe.

## Supported versions

gaal is pre-1.0 and moves quickly. Security fixes land on the latest release.

| Version | Supported |
|---------|-----------|
| Latest release | Yes |
| Older releases | No |

Update to the latest release before reporting, in case the issue is already fixed.

## Reporting a vulnerability

**Please do not report security vulnerabilities through public GitHub issues or pull requests.**

Report privately, either way:

1. **GitHub private reporting (preferred)**: open the [Security tab](https://github.com/getgaal/gaal/security) and choose **Report a vulnerability**. This creates a private advisory visible only to you and the maintainers.
2. **Email**: hello@getgaal.com.

Please include:

- A description of the vulnerability and its impact.
- Steps to reproduce, or a proof of concept.
- The gaal version (`gaal version`) and your operating system.
- Any suggested remediation, if you have one.

## What to expect

- We acknowledge your report within **3 business days**.
- We keep you informed as we investigate and work on a fix.
- We practice coordinated disclosure: we agree on a timeline with you and, unless you prefer otherwise, credit you in the release notes once a fix ships.

Please give us a reasonable window to release a fix before any public disclosure.

## Scope

gaal clones repositories, writes to agent config files, and shells out to VCS tools (`hg`, `svn`, `bzr`) for some backends. Reports that are especially valuable include arbitrary file writes outside the intended targets, command injection through config values, path traversal, and mishandling of secrets referenced in config. The `scripts/install.sh` curl-to-shell installer is also in scope.
