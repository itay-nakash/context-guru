# cmdfilter

!!! info "Offload — lossy, reversible"
    Shrinks tool output with declarative DSL filters, stashing the original and appending a recovery hint only when the filter was actually lossy.

## How it works

`cmdfilter` shrinks tool output with **declarative DSL filters** (see
[The DSL filter engine](dsl.md)). It matches a filter on the output's first non-empty line, applies
its 8-stage pipeline, stashes the original, and appends a recovery hint only when the filter was
actually lossy. It is `Enabled` only when ≥1 filter is loaded.

Deterministic filtering costs nothing — no LLM call, ~0 latency — and it is cache-safe: it acts on
the newest tool output, in the mutable tail.

### Selectors match OUTPUT, not commands

The shipped filters are adapted from [rtk](https://github.com/rtk-ai/rtk) (Apache-2.0 — see
`THIRD-PARTY-NOTICES`), with one systematic rewrite. rtk is a shell hook: it matches a **command
string** (`^terraform\s+plan`). A proxy never sees the command — it sees the *result*. So every
filter here matches an **output-shape signature** against the output's first non-empty line
(`^Refreshing state`, `^> Task :`, `^==> Downloading`). rtk's command regexes are not portable as
written; copied over they would compile fine and never fire.

That is also the structural advantage: rtk's hook only sees Bash calls, so an agent's built-in
`Read`/`Grep`/`Glob` tools are invisible to it. A proxy sees every tool result regardless of origin.

A `TestEveryBuiltinFilterHasTestsAndRoutes` guardrail asserts every filter's own test input actually
routes to that filter, so a selector rewrite is verified rather than hoped for.

### Size floor

Below `min_size` bytes (default **500**, rtk's `MIN_TEE_SIZE`) `cmdfilter` doesn't filter at all —
the recovery marker routinely costs more tokens than the saving. The marker-inclusive never-worse
check would reject those rewrites anyway; the floor skips the work and the stash instead.

## The shipped filter set

23 filters. Compression is measured on each filter's own fixtures (its inline tests), summed:

| filter | family | preserves | drops | saved |
|---|---|---|---|--:|
| `pytest` | tests | failures, summary line | `PASSED` lines, progress dots, session header | 57% |
| `npm-install` | pkg | added/removed counts, vulnerability counts | `npm warn`, spinner lines | 46% |
| `make` | builds | compiler lines, errors | `Entering/Leaving directory`, `Nothing to be done` | 56% |
| `gradle` | builds | executed tasks, test results, BUILD result | `UP-TO-DATE`/`NO-SOURCE`/`FROM-CACHE` tasks, daemon + download chatter | 49% |
| `xcodebuild` | builds | errors, warnings, test results, BUILD result | 31 build-phase and tool-invocation patterns | 62% |
| `gcc` | builds | **every** error and warning, with its source context | include-chain traces, `N warnings generated` counters | 27% |
| `swift-build` | builds | diagnostics; collapses a clean build to `ok` | `Compiling`/`Linking` lines | 29% |
| `dotnet-build` | builds | diagnostics; collapses a clean build to `ok` | MSBuild banner, restore chatter | 54% |
| `turbo` | builds | task output, errors | cache hit/miss/bypass, scope + duration lines | 66% |
| `nx` | builds | task output, errors | `> NX Running target`, log links, rule bars | 75% |
| `terraform-plan` | iac | planned changes, `Plan:` line, no-change result | `Refreshing state`, state locks, `# (N unchanged …)` | 53% |
| `terraform-init` | iac | the initialization result, errors | provider download/install lines | 72% |
| `pulumi` | iac | resource rows, outputs, resource counts, error messages | banners, permalinks, per-resource progress rows, JS stack frames | 59% |
| `liquibase` | builds | version, changeset status, errors | ASCII banner, jar inventory, INFO chatter | 76% |
| `ssh` | net | the remote command's output | `debug1:` flood, host-key and connection banners | 52% |
| `ping` | net | the statistics block, timeouts | per-packet replies | 62% |
| `rsync` | net | errors; collapses a clean sync to `ok` | file list, byte counters | 48% |
| `bundle-install` | pkg | installs, conflicts; collapses a complete bundle | `Using <gem>` lines, metadata fetch | 81% |
| `poetry-install` | pkg | lock writes, solver errors; collapses an up-to-date lock | download/install lines, virtualenv chatter | 70% |
| `composer-install` | pkg | lock writes, warnings; collapses a no-op install | download/install lines | 75% |
| `uv-sync` | pkg | the installed-package list; collapses an audited-only sync | download/cache lines | 51% |
| `brew-install` | pkg | the install summary; collapses an already-installed formula | download/pour/progress lines | 59% |
| `quarto-render` | builds | errors, warnings; collapses a successful render | per-file processing and pandoc lines | 54% |

`terraform-plan` and `make` additionally assert a **≥60% floor** on a realistic large fixture
(`TestCompressionFloors`), matching the floors rtk asserts for its equivalents.

### Every success-collapse carries an `unless` guard

Nine of rtk's eleven `match_output` success-collapse rules are unguarded — a build that emits a
warning *and* a success marker collapses to `ok` and the warning is gone. rtk learned this itself
(its `swift-build` test is named "warnings not swallowed when Build complete present"). In a proxy
the stakes are higher: the agent cannot re-run the command to find out. So every collapse rule here
carries an `unless`, plus an explicit negative test proving a warning + success marker does **not**
collapse. `TestEveryMatchOutputRuleIsGuarded` fails the build if one is added without a guard.

`dotnet-build`'s guard is worth noting: dotnet prints `0 Error(s)` on success, so a guard on the
word "error" would never let it collapse. It guards on the diagnostic *form* instead
(`error CS1002` / `warning CS0168`).

### Line budgets: shared `cap` classes

Filters select a budget by **signal density** (`cap: errors`) rather than each hand-picking a
`max_lines`, so the whole set is tunable from one map (`dsl.Caps`). See
[the DSL engine](dsl.md#line-budgets-cap-classes).

## What is deliberately NOT ported

rtk ships 63 DSL filters and ~50 native Rust ones. 23 filters are ported. The rest is excluded on
purpose:

- **The ~24 `truncate_lines_at`-only filters** (`df`, `ps`, `du`, `jq`, `jira`, `markdownlint`,
  `yamllint`, `stat`, `gcloud`, `helm`, `iptables`, `skopeo`, `yadm`, `hadolint`, …). Their whole
  "filter" is "strip blank lines + cap line width" — whole-blob lossy for a modest, unmeasured
  saving. If that effect is wanted, one generic width-cap filter beats 24 files.
- **The blank-line-only linter filters** (`shellcheck`, `systemctl-status`, `sops`, `fail2ban`,
  `basedpyright`, `ty`, `oxlint`, `biome`, `mix-format`, `tofu-fmt`, `tofu-validate`). Same reason;
  the useful part is their `on_empty` collapse, which one or two generic linter filters can carry.
- **`spring-boot`** — a `keep_lines_matching` allowlist over unbounded application logs. Too easy
  to drop the one line that mattered, and nothing recovers it but a full expand.
- **`filter_stderr`** — no proxy analogue; by the time output reaches a proxy the streams are
  already merged.
- **rtk's 86 command-detection rules** — entirely about rewriting shell commands to `rtk <cmd>`.
  Irrelevant to a proxy. (Its per-tool `savings_pct` figures are hand-estimated, not measured, so
  they are not reused as expected-gain data either.)
- **rtk's ~50 native Rust filters** (43,850 lines: `--format json` → parse → re-render for cargo,
  rubocop, golangci, ruff, phpstan; TRX/binlog parsing; git diffstat compression). Its
  highest-compression technique, but inexpressible in the DSL and mostly unreachable from a proxy
  that cannot inject `--format json` into a command it never sees. The output-side half — a JSON or
  TRX blob that *arrives* as a tool result and could be re-rendered — remains open.

## Observability

Savings are attributed **per command family** and per filter in `/stats`, cumulative and unique
(deduped by content key, since the agent re-sends history verbatim every turn):

```json
"cmdfilter_families": { "iac": {"acts": 3, "saved_tokens": 640, "saved_tokens_unique": 340} },
"cmdfilter_filters":  { "terraform-plan": {"acts": 2, "saved_tokens": 600, "saved_tokens_unique": 300} },
"cmdfilter_selector_misses": [ {"selector": "Some unrecognized first line", "count": 7} ]
```

`cmdfilter_selector_misses` is the **ledger of output shapes that matched no filter**, ranked by
frequency (after rtk's `parse_failures` table). It makes "which filter to write next" data instead
of guesswork. The ledger is bounded at 200 distinct selectors.

## Before → After

```
before:  terraform plan … 40 "Refreshing state" lines + state locks + unchanged-attribute comments
after:   Terraform will perform the following actions:
           # aws_instance.web will be created
         Plan: 1 to add, 0 to change, 0 to destroy.
         <<cg:…>> [full output: call context_guru_expand]
```

## Lossiness

Lossy but reversible — the original is stashed and recovered via `context_guru_expand` /
`GET /expand`. A recovery hint is appended only when the filter actually dropped content, and it is
**typed by what was lost**: a clean contiguous tail cut names the cut point (cheap partial recovery),
a whole-blob loss points at the expand tool. See [the DSL engine](dsl.md#lossiness).

## Configuration

| Key | Default | Meaning |
|---|---|---|
| `filters` | `[]` | Inline filter YAML docs, added with no recompile. |
| `disable_builtins` | `false` | Disable the shipped filter set and run only your own. |
| `marker_mode` | `full` | `full` (stash + resolvable marker) / `summary` / `off`. |
| `min_size` | `500` | Byte floor; smaller outputs are left alone. |

## When it shines

Noisy but structured command output: build tools, test runners, package managers, IaC plans, verbose
network clients.

## When it's inert

Output whose first line matches no filter (logged as a selector miss), output under `min_size`, or
filtering that doesn't shrink the message once the marker is counted.

See also: [Components overview](../components.md) · [The DSL filter engine](dsl.md) ·
[Write a custom DSL filter](../how-to/custom-dsl-filter.md) ·
[Choose a preset](../how-to/choose-a-preset.md)
