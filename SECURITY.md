# Security Policy

## Reporting a vulnerability

Please report security vulnerabilities privately. Do **not** open a public GitHub issue.

Use GitHub's private vulnerability reporting ("Report a vulnerability" under the
repository's Security tab), or follow the disclosure process described in the main
[Kagenti](https://github.com/kagenti/kagenti/blob/main/SECURITY.md) repository.

We will acknowledge your report and work with you on a coordinated disclosure.

## Scope notes

This component intercepts and rewrites LLM agent traffic. It is designed to **fail open**:
on any internal error it forwards the original request unmodified. It does not persist
credentials. Reduction state (the rewind store) is content-addressed and node-local by
default; treat any configured shared backend (e.g. Redis) as sensitive.
