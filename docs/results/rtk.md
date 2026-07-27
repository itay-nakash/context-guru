# Full results — rtk (Rust Token Killer) (SWE-bench Verified, 50 tasks)

Live through the harness, `claude-code` agent on `aws/claude-sonnet-5`. Cache-aware billed input cost (fresh $2/M · cache-read $0.20/M · cache-write $2.50/M) + output $10/M, recomputed from each trial's token tiers. See [REPRODUCE.md](REPRODUCE.md).

## Totals

| tasks scored | solved | rate | total billed cost | mean steps | cache-hit | agent wall (sum) |
|---|---|---|---|---|---|---|
| 50 | 43 | 86% | $29.09 | 33.2 | 97.9% | 320 min |

**vs the no-compaction baseline** ($31.98 / 43 solved / 36.1 steps): rtk is **−9.0%**
billed cost, **reward-neutral** (43 = 43), **−8%** steps, at **zero request-path latency**
and **$0 tool cost**. It is the 2nd-cheapest of the four arms — cheaper than headroom
(−5.3%), behind context-guru (−13.2%). Full four-way table: [comparison.md](comparison.md).

## What rtk did (its own ledger)

rtk is a Claude Code `PreToolUse` hook that rewrites Bash commands in-container
(`pytest`→`rtk pytest`, `cat`→`rtk read`, `git status`→`rtk git status`, …) and compresses
the command output **at the shell, before it enters the transcript**. Because the compressed
form is what gets cached from the first turn, rtk **never mutates already-cached content** —
it sidesteps cache invalidation by construction (lowest cache-write of the four arms, 1.83M).

Across the 50 clean trials it rewrote **637 bash commands** and removed **338k bash-output
tokens (65.8% of bash output**, its own `bytes/4` estimate). Note the denominator: that
65.8% is of *bash output only* — a small slice of a ~98%-cached agent's total context —
which is why the end-to-end saving is −9% billed cost, not −66%. (This is rtk's own
documented caveat: it cuts bash output, not the bill, one-to-one.)

| rtk command | invocations | ~bash tokens removed | share |
|---|--:|--:|--:|
| `rtk read` (`cat`) | 39 | ~209,000 | 62% |
| `rtk grep` | 285 | ~80,400 | 24% |
| `rtk git` (status/diff/stash) | 134 | ~20,800 | 6% |
| `rtk ls` | 44 | ~12,600 | 4% |
| `rtk pytest` | 57 | ~12,000 | 4% |
| `rtk diff` / `pip` / `wc` / `find` / `curl` | 81 | ~3,100 | 1% |

Savings concentrate in **file reads via `cat`** (62%) and **`grep`** (24%, the most-invoked
command). Claude Code's built-in `Read`/`Grep`/`Glob` tools bypass the Bash hook, so rtk only
sees output the agent routed through Bash — this is the structural ceiling on its reach.

### Real before → after (rtk is deterministic; these reproduce its in-container behavior)

**`cat` a source file → `rtk read -l aggressive`** (332 B → 164 B): keeps imports + every
signature, elides bodies.
```
import os
import sys
def alpha(a, b):
    // ... implementation
def beta(x):
    // ... implementation
class Widget:
    def render(self):
    // ... implementation
```

**a failing `pytest` run → `rtk test`** (1,055 B → 195 B, ~81%): failures + summary kept,
213 passing lines dropped.
```
[FAIL] FAILURES:
  FAILED tests/test_utils.py::test_parse_edge_case - AssertionError: assert None...
SUMMARY:
  ======================== 1 failed, 213 passed in 4.21s =========================
```

## Per-task

| task | reward | steps | cache_read | cache_write | billed cost |
|---|---|---|---|---|---|
| astropy__astropy-12907 | 1 | 14 | 616,309 | 8,012 | $0.180 |
| astropy__astropy-14365 | 1 | 31 | 1,761,659 | 46,069 | $0.590 |
| astropy__astropy-8707 | 0 | 65 | 3,821,411 | 48,206 | $1.039 |
| django__django-11095 | 1 | 27 | 1,230,383 | 26,847 | $0.357 |
| django__django-11211 | 1 | 48 | 2,844,253 | 45,130 | $0.865 |
| django__django-11477 | 1 | 41 | 2,111,490 | 32,771 | $0.634 |
| django__django-11790 | 1 | 21 | 971,393 | 16,828 | $0.280 |
| django__django-12050 | 1 | 14 | 571,174 | 17,935 | $0.185 |
| django__django-12308 | 1 | 23 | 1,177,172 | 32,079 | $0.355 |
| django__django-12858 | 1 | 51 | 2,876,052 | 39,900 | $0.903 |
| django__django-13128 | 1 | 51 | 3,066,423 | 75,346 | $1.005 |
| django__django-13363 | 1 | 16 | 791,803 | 32,944 | $0.281 |
| django__django-13568 | 1 | 25 | 1,137,721 | 26,294 | $0.349 |
| django__django-13810 | 1 | 31 | 1,771,868 | 40,978 | $0.534 |
| django__django-14034 | 0 | 25 | 1,141,289 | 59,052 | $0.501 |
| django__django-14349 | 1 | 22 | 992,170 | 24,206 | $0.306 |
| django__django-14559 | 1 | 25 | 1,162,737 | 29,116 | $0.344 |
| django__django-14792 | 1 | 23 | 1,195,900 | 26,221 | $0.381 |
| django__django-15128 | 1 | 51 | 3,252,766 | 197,694 | $1.302 |
| django__django-15380 | 1 | 25 | 1,187,466 | 26,767 | $0.367 |
| django__django-15572 | 1 | 19 | 832,903 | 22,241 | $0.275 |
| django__django-15930 | 1 | 39 | 2,047,139 | 32,769 | $0.604 |
| django__django-16145 | 1 | 29 | 1,404,848 | 27,753 | $0.431 |
| django__django-16502 | 1 | 51 | 2,861,378 | 39,610 | $0.853 |
| django__django-16667 | 0 | 13 | 524,431 | 17,518 | $0.166 |
| django__django-17087 | 1 | 23 | 1,018,514 | 22,426 | $0.298 |
| matplotlib__matplotlib-22719 | 1 | 33 | 1,675,521 | 29,577 | $0.486 |
| matplotlib__matplotlib-24570 | 1 | 17 | 758,768 | 21,599 | $0.287 |
| matplotlib__matplotlib-25775 | 1 | 102 | 7,436,574 | 67,271 | $1.968 |
| psf__requests-1142 | 1 | 15 | 626,137 | 20,165 | $0.213 |
| pydata__xarray-3151 | 1 | 25 | 1,202,343 | 27,163 | $0.359 |
| pydata__xarray-4966 | 1 | 30 | 1,562,847 | 34,090 | $0.475 |
| pylint-dev__pylint-4551 | 1 | 75 | 5,995,589 | 74,292 | $1.614 |
| pytest-dev__pytest-10051 | 1 | 15 | 628,585 | 18,041 | $0.202 |
| pytest-dev__pytest-7205 | 1 | 46 | 2,494,414 | 38,266 | $0.713 |
| scikit-learn__scikit-learn-10844 | 1 | 6 | 207,791 | 15,094 | $0.090 |
| scikit-learn__scikit-learn-13328 | 1 | 16 | 683,722 | 21,121 | $0.223 |
| scikit-learn__scikit-learn-14894 | 1 | 21 | 915,359 | 19,868 | $0.278 |
| scikit-learn__scikit-learn-9288 | 1 | 18 | 816,867 | 56,210 | $0.361 |
| sphinx-doc__sphinx-7454 | 1 | 7 | 266,124 | 20,532 | $0.120 |
| sphinx-doc__sphinx-8120 | 1 | 80 | 4,876,037 | 54,263 | $1.384 |
| sphinx-doc__sphinx-8638 | 0 | 50 | 2,855,816 | 40,367 | $0.920 |
| sphinx-doc__sphinx-9602 | 0 | 10 | 390,339 | 18,665 | $0.184 |
| sympy__sympy-13031 | 0 | 17 | 737,132 | 20,161 | $0.292 |
| sympy__sympy-13877 | 1 | 22 | 1,114,720 | 32,317 | $0.412 |
| sympy__sympy-15599 | 1 | 73 | 4,190,786 | 43,343 | $1.209 |
| sympy__sympy-17318 | 1 | 52 | 2,768,890 | 31,688 | $0.805 |
| sympy__sympy-19495 | 1 | 54 | 3,092,829 | 42,633 | $1.368 |
| sympy__sympy-21379 | 1 | 43 | 2,317,605 | 36,288 | $0.677 |
| sympy__sympy-23413 | 0 | 32 | 1,683,504 | 37,181 | $1.063 |
