# Token Command Setup

onWatch can obtain a provider access token by running a command instead of reading a token from a local credential file or keychain. Configure one of these variables in `~/.onwatch/.env`:

- `ANTHROPIC_TOKEN_COMMAND`
- `CODEX_TOKEN_COMMAND`
- `CURSOR_TOKEN_COMMAND`

The command is run through `/bin/sh -c` on Unix and `cmd /C` on Windows. Shell expansion is available, including `$HOME` and other environment variables. onWatch waits at most 30 seconds and uses trimmed standard output as the token.

A command-backed provider is enabled when its command returns a token at startup. The command runs at startup, before each usage poll, and after a 401 response. If it exits non-zero, writes nothing, or times out, onWatch logs the error and keeps the previous token. A failed startup command leaves the provider unconfigured unless another supported credential source supplies a token. onWatch never writes command-managed tokens back to disk or a keychain, and does not use the provider's OAuth self-refresh path.

## Examples

Use any secret manager that prints a value to standard output:

```dotenv
ANTHROPIC_TOKEN_COMMAND=op read 'op://Vault/Item/token'
CODEX_TOKEN_COMMAND=pass show path/to/codex-token
```

The OMP auth broker can provide a selected account directly:

```dotenv
ANTHROPIC_TOKEN_COMMAND=omp token anthropic --account 1
```

For OMP, `omp token anthropic --list` prints numbered accounts such as `1. me@example.com (Example Org)`. Choose the account number for the command rather than putting a token in `.env`.

## Explicit initial tokens

When both `*_TOKEN` and `*_TOKEN_COMMAND` are set, the explicit `*_TOKEN` wins for the initial value. The command still becomes the refresh hook for subsequent polls and 401 responses.

Keep command output limited to the token. Diagnostics should go to standard error so they do not become part of the token.
