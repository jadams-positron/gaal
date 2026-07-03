# `gaal skill`

Search and install registry-backed skills.

```sh
gaal skill search react
gaal skill search react --limit 10 --registry skills.sh
gaal skill install frontend-design
gaal skill install frontend-design --project
gaal skill install anthropics/skills:frontend-design --agents codex,claude-code
```

## Search

`gaal skill search <query>` queries one registry. The unmanaged default is
`skills.sh`; `--limit` defaults to `10` and accepts `1..100`.
For `skills.sh`, gaal shells out to the public npm CLI with
`npx -y skills find <query>`. This requires `npx` on `PATH`, but it does not
require credentials.

The command is config-optional and supports the global output flag:

```sh
gaal skill search react -o json
```

## Install

`gaal skill install <ref>` resolves a registry reference, updates the selected
`gaal.yaml` (`--config`), and runs the normal one-shot sync flow by default.

Accepted reference forms:

```sh
gaal skill install frontend-design
gaal skill install skills.sh/frontend-design
gaal skill install anthropics/skills:frontend-design
gaal skill install skills.sh/anthropics/skills:frontend-design
```

Registry installs write normal `skills:` entries plus provenance:

```yaml
skills:
  - source: anthropics/skills
    registry: skills.sh
    select:
      - frontend-design
    agents: ["*"]
    global: true
```

Scope behavior:

| Flag | Effect |
|------|--------|
| _(default)_ | write `global: true` |
| `--global` | write `global: true` explicitly |
| `--project` | write `global: false` |

`--global` and `--project` are mutually exclusive.

Other install flags:

| Flag | Effect |
|------|--------|
| `--agents a,b` | write explicit target agents after validating names |
| `--no-sync` | update YAML only |
| `--yes` | skip confirmation prompts |

If `--agents` is omitted in an interactive terminal, gaal prompts for targets
with `["*"]` as the default. In non-interactive mode, install requires `--yes`.

If a loose reference is ambiguous, interactive terminals show a picker.
Non-interactive runs print candidate refs and exit without mutating files.
