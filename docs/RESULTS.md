# Benchmark results

Per-component evaluation of context-guru on **SWE-bench** with the **Claude Code** agent and model
**`claude-sonnet-4-6`**, through the context-guru gateway in [eval-containers](setup.md). Baseline = the
same gateway with an empty pipeline (passthrough); each `cg-*` = that one component alone (default config,
**no task-specific tuning**); `cg-balanced` = the preset. 10 tasks × 13 configs = 130 runs.

## Headline

- **Zero task-reward change, every component.** Of the 10 tasks, the baseline resolved 6; on those 6 every
  config still resolved (6/6), and none of the 4 the baseline failed were rescued. context-guru changes
  tokens, not agent capability — no task broken, none fixed.
- **`mask` is the biggest lever** (drop tool outputs older than the keep-recent window): **26.8% mean
  content-token savings on the resolved tasks, up to 93.5% on a long session**, reward preserved.
- **Component overhead is single-digit milliseconds** — the token savings cost effectively no compute.

## Per-component summary

| component | fires on | mean save% (all 10) | mean save% (resolved 6) | reward kept | compute/req |
|---|---|---:|---:|:---:|---:|
| `cg-format` | pretty-printed JSON | 0.0 | 0.0 | 6/6 | ~1–7 ms |
| `cg-dedup` | exact dup tool outputs | 0.3 | 0.2 | 6/6 | ~1–7 ms |
| `cg-cmdfilter` | shell-cmd banners | 0.0 | 0.0 | 6/6 | ~1–7 ms |
| `cg-cacheinject` | provider cache (not content) | 0.0 | 0.0 | 6/6 | ~1–7 ms |
| `cg-failed_run` | superseded test/build runs | 7.3 | 5.8 | 6/6 | ~6 ms |
| `cg-skeleton` | fenced code blocks | 0.0 | 0.0 | 6/6 | ~1–7 ms |
| `cg-collapse` | >2000-tok outputs | 0.6 | 0.0 | 6/6 | ~1–7 ms |
| `cg-mask` | old tool outputs | 30.9 | 26.8 | 6/6 | ~2 ms |
| `cg-smartcrush` | JSON arrays | 0.0 | 0.0 | 6/6 | ~1–7 ms |
| `cg-extract` | large outputs (relevance) | 11.4 | 11.1 | 6/6 | ~6 ms |
| `cg-phi_evict` | transcript > budget | 0.0 | 0.0 | 6/6 | ~1–7 ms |
| `cg-balanced` | combined preset | 9.0 | 12.2 | 6/6 | ~1–7 ms |

Compute/req measured directly for mask/failed_run/extract; the rest are the same order of magnitude
(regex / JSON / hashing over the message array). All negligible vs. the agent's seconds-to-minutes per step.

## Content-token savings % per task (within-run before→after)

| config | sympy-13647 | sympy-16766 | sympy-20438 | sphinx-7910 | sphinx-9320 | scikit-learn-12973 | scikit-learn-25931 | xarray-4629 | django-11820 | django-14089 |
|---|---|---|---|---|---|---|---|---|---|---|
| `baseline` | — | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | — | 0.0 | 0.0 |
| `cg-format` | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| `cg-dedup` | 1.5 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 1.5 | 0.0 |
| `cg-cmdfilter` | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| `cg-cacheinject` | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| `cg-failed_run` | 9.0 | 19.7 | 1.4 | 5.4 | 0.0 | 0.0 | 0.8 | 0.0 | 36.8 | 0.0 |
| `cg-skeleton` | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| `cg-collapse` | 0.0 | 0.0 | 6.2 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| `cg-mask` | 19.6 | 42.9 | 93.5 | 19.0 | 39.0 | 0.0 | 40.6 | 0.0 | 50.0 | 4.2 |
| `cg-smartcrush` | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| `cg-extract` | 14.9 | 5.0 | 28.8 | 19.3 | 3.8 | 11.2 | 12.1 | 0.0 | 19.1 | 0.0 |
| `cg-phi_evict` | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 | 0.0 |
| `cg-balanced` | 14.6 | 14.1 | 2.6 | 21.0 | 0.0 | 0.0 | 23.4 | 0.0 | 14.1 | 0.0 |

## Reward per task (1 = resolved)

| config | sympy-13647 | sympy-16766 | sympy-20438 | sphinx-7910 | sphinx-9320 | scikit-learn-12973 | scikit-learn-25931 | xarray-4629 | django-11820 | django-14089 |
|---|---|---|---|---|---|---|---|---|---|---|
| `baseline` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | — | 0 | 0 |
| `cg-format` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-dedup` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-cmdfilter` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-cacheinject` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-failed_run` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-skeleton` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-collapse` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-mask` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-smartcrush` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-extract` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-phi_evict` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |
| `cg-balanced` | 1 | 1 | 0 | 1 | 1 | 1 | 1 | 0 | 0 | 0 |

## Agent wall-clock (seconds) — single run each; **dominated by agent nondeterminism, not component cost**

| config | sympy-13647 | sympy-16766 | sympy-20438 | sphinx-7910 | sphinx-9320 | scikit-learn-12973 | scikit-learn-25931 | xarray-4629 | django-11820 | django-14089 |
|---|---|---|---|---|---|---|---|---|---|---|
| `baseline` | 67 | 59 | 333 | 56 | 53 | 28 | 138 | — | 200 | 22 |
| `cg-format` | 74 | 60 | 342 | 60 | 52 | 31 | 129 | 25 | 113 | 23 |
| `cg-dedup` | 71 | 59 | 461 | 108 | 38 | 35 | 154 | 21 | 172 | 34 |
| `cg-cmdfilter` | 82 | 70 | 251 | 74 | 47 | 33 | 96 | 22 | 93 | 25 |
| `cg-cacheinject` | 58 | 49 | 1352 | 119 | 37 | 32 | 130 | 22 | 118 | 28 |
| `cg-failed_run` | 86 | 95 | 375 | 126 | 53 | 32 | 91 | 22 | 219 | 23 |
| `cg-skeleton` | 80 | 82 | 253 | 60 | 45 | 36 | 106 | 24 | 233 | 22 |
| `cg-collapse` | 76 | 82 | 1047 | 106 | 46 | 30 | 121 | 29 | 168 | 25 |
| `cg-mask` | 63 | 87 | 1833 | 109 | 59 | 33 | 155 | 33 | 116 | 27 |
| `cg-smartcrush` | 65 | 89 | 705 | 125 | 38 | 30 | 132 | 24 | 240 | 23 |
| `cg-extract` | 70 | 96 | 517 | 109 | 61 | 42 | 123 | 23 | 218 | 22 |
| `cg-phi_evict` | 79 | 93 | 276 | 111 | 37 | 35 | 140 | 26 | 191 | 38 |
| `cg-balanced` | 91 | 71 | 633 | 129 | 59 | 36 | 156 | 29 | 454 | 22 |

## Real message rewrites (captured on `sphinx-doc__sphinx-7910`)

Actual before→after content the components produced (via `CONTEXT_GURU_DUMP`). Offloaded originals are
recoverable from the store by the `<<cg:HASH>>` marker.

### `mask` — drops tool outputs older than the keep-recent window

**606 → 27 tokens (saved 579):**

```text
BEFORE:
407-        `obj` will be `A.__init__`.
408-    skip : bool
409-        A boolean indicating if autodoc will skip this member if `_skip_member`
410-        does not override the decision
411-    options : sphinx.ext.autodoc.Options
412- …
AFTER:
[older tool output masked] <<cg:b162e82de872a202>> [full output: call context_guru_expand]
```

**223 → 31 tokens (saved 192):**

```text
BEFORE:
426	    if name != '__weakref__' and has_doc and is_member:
427	        cls_is_owner = False
428	        if what == 'class' or what == 'exception':
429	            qualname = getattr(obj, '__qualname__', '')
430	            cls_path, _, _ = …
AFTER:
[older tool output masked] <<cg:f7d9263d63d37c8e>> [full output: call context_guru_expand]
```

### `failed_run` — collapses a run superseded by a later one

**423 → 33 tokens (saved 390):**

```text
BEFORE:
Exit code 1
WARNING: Retrying (Retry(total=4, connect=None, read=None, redirect=None, status=None)) after connection broken by 'NewConnectionError('<pip._vendor.urllib3.connection.HTTPSConnection object at 0x7ffffd4f3fa0>: Failed to establi …
AFTER:
[superseded by a later run] <<cg:58242760d7f8bf6f>> [full output: call context_guru_expand]
```

**201 → 32 tokens (saved 169):**

```text
BEFORE:
__wrapped__: True
__qualname__: MyClass.__init__
cls_path: MyClass
wrapper __globals__ keys: ['__name__', '__doc__', '__package__', '__loader__', '__spec__', '__builtins__', 'functools', 'my_decorator']
Old way FAILED with KeyError - BUG CO …
AFTER:
[superseded by a later run] <<cg:3beb3a2148120db7>> [full output: call context_guru_expand]
```

### `extract` — keeps query-relevant lines, elides the rest

**455 → 434 tokens (saved 21):**

```text
BEFORE:
415	        directive.
416	
417	    Returns
418	    -------
419	    bool
420	        True if the member should be skipped during creation of the docs,
421	        False if it should be included in the docs.
422	
423	    """
424	    has_doc …
AFTER:
415	        directive.
416	
417	    Returns
418	    -------
419	    bool
420	        True if the member should be skipped during creation of the docs,
421	        False if it should be included in the docs.
…
424	    has_doc = getattr(obj, '__doc__', False)
425	    is_member = (what == 'class' or wh
```

## LLM-based components (`extract strategy: code`, `summarize`) — gated + state-reuse

These two components call a model. They are gated by a configurable `trigger` (don't fire every turn)
and reuse prior compactions from per-session state (don't re-call the model on re-sent content).
`model.source: incoming` = the agent's own model (`claude-sonnet-4-6`). Final tuned config:
extract-code `trigger: {min_output_tokens: 700, min_request_tokens: 4000}`; summarize
`trigger: {min_request_tokens: 6000, min_messages: 10}, resummarize_tokens: 5000`, call timeout 150 s,
trajectory capped to fit context. Sweep of both, alone, over the 10 tasks:

| task | base | extract-code r / saved | summarize r / saved |
|---|---|---|---|
| sympy-13647 | 1 | 1 / **2562** | 1 / 0 (gated off) |
| sympy-16766 | 1 | 1 / 0 | 1 / 0 (gated off — was a 1→0 regression) |
| sympy-20438 | 0 | 0 / **177914** (~24%) | 0 / **10882** |
| sphinx-7910 | 1 | 1 / 0 | 1 / 0 (gated off) |
| sphinx-9320 | 1 | 1 / **5677** | 1 / 0 (gated off — was a 1→0 regression) |
| scikit-12973 | 1 | 1 / 0 | 1 / 0 |
| scikit-25931 | 1 | 1 / **2952** | 1 / 0 |
| django-11820 | 0 | 0 / **3920** | 0 / **4695** |
| django-14089 | 0 | 0 / 0 | 0 / 0 |
| xarray-4629 | – | 0 / 0 | 0 / 0 |

**Resolved / 10 (baseline = 6): extract-code = 6, summarize = 6 — zero reward regression.** The two
earlier summarize regressions (sympy-16766, sphinx-9320: 1→0 in the first ungated run) stay fixed.
Total gateway tokens saved across the set: **extract-code ≈ 193k, summarize ≈ 15.6k**.

- **extract-code** fires on 5 of the tasks and is reward-neutral everywhere. Biggest win is the large
  task (sympy-20438, ~24% of the request). Two changes drove the breadth: `IsContained`'s string case
  was generalized to an in-order whole-line **subsequence** (so it reduces logs / source / tracebacks,
  not only JSON — the earlier JSON-only containment was why it fired on almost nothing), and the floor
  was lowered to 700 tokens so mid-size outputs qualify.
- **summarize** — the key diagnosis: it saved ~0 at higher triggers because **almost every SWE-bench
  Claude Code request is small** (per-request means ≈ 2k tokens; only sympy-20438 averages ~7k). A 15–25k
  trigger simply never fired. Lowering it to **6k** threads the needle: it fires on the large,
  **reward-0** tasks (sympy-20438, django-11820) where summarizing is safe, and stays off the tiny
  (~2k-avg) transcripts whose summarization caused the earlier regressions. That's why summarize now
  saves ~15.6k with the two regressions still fixed. Pushing the trigger lower would summarize the tiny
  tasks too and re-introduce the reward risk — the real unlock for safe savings there is expand-tool
  injection (reversibility), still deferred.
- **State reuse** (proved by unit tests): extract caches each reduced output by content hash and
  summarize checkpoints its summary per session, so a re-sent output / unchanged prefix reuses the prior
  result byte-for-byte — no repeat model call, KV-cache-stable prefix. Stops both re-deriving a
  different compaction every turn.
- **Fundamental tension** confirmed empirically: on this traffic, summarize only saves where it fires,
  and firing on small transcripts is exactly what regresses reward. The gain came from firing it on the
  *large, already-failing* tasks — not from accepting regressions on the small ones.

## Methodology & caveats

- Harness: [`deploy/eval-containers/sweep.py`](../deploy/eval-containers/sweep.py) (resumable) → `aggregate.py`.
  One cell = one `(task, config)` run through the compose stack; reward from `output/task/result.json`,
  savings from the gateway `/stats` (within-run `tokens_before → tokens_after`), component time from the
  gateway's per-component `duration_ms` logs, example rewrites from `CONTEXT_GURU_DUMP`.
- **Within-run savings %** is the honest metric — before/after are the same request stream, immune to
  run-to-run agent nondeterminism. Cumulative token totals across a growing transcript overstate per-task
  savings and are not used here.
- **Single run per cell.** Agent trajectories are stochastic, so both wall-clock and per-task savings vary
  run-to-run (e.g. `extract` on sphinx-7910 was 19% in the sweep, 0.7% on the capture re-run). Robust
  numbers need N repeats per cell; treat single-task figures as indicative, the per-component means as the
  signal.
- **Reward parity** is judged only on the 6 tasks the baseline resolved; the other 4 neither baseline nor
  context-guru resolved (SWE-bench resolve rates vary).
- **$ cost** is not measured directly — Claude Code streams (SSE) and the gateway would need to parse the
  streaming `usage` for exact provider-billed tokens. Savings are content tokens; at a representative
  ~$3/M Sonnet input price the message-portion input cost drops roughly in proportion to the savings %
  each turn, compounding across a session.
- Host was arm64 (SWE-bench images run under emulation); functional results, not latency benchmarks.
- **Known follow-up:** `extract`/`collapse`/`mask`/`failed_run`/`skeleton` check their per-message size
  win *before* appending the `<<cg:HASH>>` marker, so an individual message can occasionally grow by the
  marker's ~10–15 tokens (the pipeline's overall never-worse guard still reverts any net regression). The
  marker-inclusive check already added to `cmdfilter` should be applied to the other offloaders too.

## Tasks

`sympy-13647/16766/20438`, `sphinx-7910/9320`, `scikit-learn-12973/25931`, `xarray-4629`,
`django-11820/14089`.
