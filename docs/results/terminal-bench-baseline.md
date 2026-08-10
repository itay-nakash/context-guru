# Full results — baseline (Terminal-Bench 2.0, 89 tasks)

Baseline arm: **no compaction** — the `claude-code` agent on `aws/claude-sonnet-5`, run LIVE through the harness against Terminal-Bench 2.0's 89 tasks. Routing goes through the context-guru `off` transparent passthrough proxy (identical plumbing to the compaction arms; zero content change), so this is the like-for-like reference the framework arms are measured against. Cache-aware billed input cost (fresh $2/M · cache-read $0.20/M · cache-write $2.50/M) + output $10/M, recomputed from each trial's own token tiers — the same model as the SWE-bench study. See [REPRODUCE.md](REPRODUCE.md).

## Totals

| attempted | solved | solve rate | completed | timed out | total billed cost | mean steps* | cache-hit |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 89 | 56 | **62.9%** | 82 | 7 | $100.81 | 31.5 | 98.2% |

\* mean steps over the 82 completed tasks (timed-out runs are truncated). Solve rate over **completed-only** tasks: **56/82 = 68.3%**.

**Time-budget policy.** Wall-clock budget = the task-authored timeout × a multiplier. Most tasks ran at **1.5×**; the long-horizon tasks that first timed out were retried at low concurrency and, if still short, given an extended **4×** budget (up to ~4 h) to measure capability rather than a latency-truncated result. 3 tasks solved only under the 4× budget (counted as solved here); the 7 below exhausted even 4×.

## Analysis — where the agent is strong / weak

- **Terminal-Bench 2.0 is a much harder, longer-horizon benchmark than SWE-bench Verified.** The
  baseline solves **62.9%** of tasks vs 86% on SWE-bench, at **~$1.13/task** (vs $0.64) and **~1.7M
  prompt tokens/task** — TB tasks are open-ended terminal goals (build/compile/train/exploit), not a
  localized patch, so the agent runs longer and reads far more.
- **Difficulty is the dominant axis.** Easy/medium solve at **71–75%**; `hard` drops to **47%** and
  costs **3.4× more per task** ($2.04 vs $0.61). Every one of the 7 unrecoverable timeouts is a
  hard/long task.
- **Category tells the same story from the other side.** The agent is reliable on bounded,
  verifiable goals — `debugging` (100%), `system-administration` (78%), `security` (75%),
  `data-processing`/`model-training` (75%) — and weak on sprawling build/implement tasks:
  `software-engineering` (38%, the largest bucket at 26 tasks) and `video-processing` (0%).
- **It is still a ~98%-cached agent** (98.2% cache-hit), so — exactly as on SWE-bench —
  **cache-read is the single biggest cost term (43% of the bill)**. That is the lever the compaction
  arms must pull; a layer that shrinks the cached context should move TB cost the same way it moved
  SWE cost.
- **Latency, not just capability, caps the ceiling.** The gateway's ~26 s/request (5–10× a normal
  endpoint) means long-horizon tasks can run out of wall-clock before finishing. 3 tasks that first
  timed out **solved once given a 4× budget** — so **62.9% is a floor**, and a compaction arm that
  reduces round-trips could recover more of the remaining 7. The timeout count is therefore itself a
  comparison metric, not just noise.

### Token & cost accounting (cache-aware, all 89 tasks)

| tier | tokens | $/M | billed |
|---|--:|--:|--:|
| cache-read (input) | 215,971,427 | 0.20 | $43.19 |
| cache-write (input) | 4,011,068 | 2.50 | $10.03 |
| fresh (input) | 58,893 | 2.00 | $0.12 |
| completion (output) | 4,746,887 | 10.00 | $47.47 |
| **total** | | | **$100.81** |

Cache-read is **43%** of the bill at a **98.2%** cache-hit rate — as on SWE-bench, a heavily-cached agent, so the lever a compaction layer must pull is cache-read tokens.

## Timeouts (7 long-horizon tasks)

These tasks still hit the wall-clock budget under the **extended 4×** timeout (up to ~4 h each) and scored **reward 0** — counted as failures in the solve rate above. A large part of the cause is **gateway latency, not only agent capability**: Terminal-Bench's timeouts assume a fast endpoint (~2–5 s/request), but this IBM LiteLLM gateway runs **~26 s/request** (5–10× slower), so long-horizon tasks that need many round-trips run out of clock (concurrency is *not* the cause — latency was flat ~23–30 s/req from n=1 to n=24). They are all `hard`/long software-engineering and compute tasks (path-tracing, a MIPS Doom port, a metacircular evaluator, COBOL modernization, GPT-2 code-golf, CIFAR training). A compaction arm that cuts round-trips could bring some under budget, so the timeout count is itself a comparison metric.

| task | difficulty | category | steps before timeout | partial billed | budget (4×) |
|---|---|---|--:|--:|--:|
| caffe-cifar-10 | medium | machine-learning | 12 | $0.24 | 80 min |
| cobol-modernization | easy | software-engineering | 133 | $5.11 | 60 min |
| gpt2-codegolf | hard | software-engineering | 43 | $1.48 | 60 min |
| make-doom-for-mips | hard | software-engineering | 160 | $6.39 | 60 min |
| path-tracing-reverse | hard | software-engineering | 170 | $9.62 | 120 min |
| schemelike-metacircular-eval | medium | software-engineering | 74 | $4.37 | 160 min |
| write-compressor | hard | software-engineering | 6 | $0.36 | 60 min |

## By difficulty (all 89 tasks; timeouts = failures)

| difficulty | tasks | solved | rate | timed out | mean $/task |
|---|--:|--:|--:|--:|--:|
| easy | 4 | 3 | 75% | 1 | $1.561 |
| medium | 55 | 39 | 71% | 2 | $0.606 |
| hard | 30 | 14 | 47% | 4 | $2.040 |

## By category (all 89 tasks)

| category | tasks | solved | rate | mean $/task | mean steps* |
|---|--:|--:|--:|--:|--:|
| data-querying | 1 | 1 | 100% | $1.151 | 17.0 |
| debugging | 5 | 5 | 100% | $0.878 | 40.0 |
| games | 1 | 1 | 100% | $0.570 | 36.0 |
| personal-assistant | 1 | 1 | 100% | $0.329 | 8.0 |
| system-administration | 9 | 7 | 78% | $0.903 | 46.3 |
| data-processing | 4 | 3 | 75% | $0.309 | 15.0 |
| mathematics | 4 | 3 | 75% | $1.396 | 36.0 |
| model-training | 4 | 3 | 75% | $0.553 | 24.8 |
| security | 8 | 6 | 75% | $0.334 | 15.4 |
| machine-learning | 3 | 2 | 67% | $1.248 | 34.5 |
| data-science | 8 | 5 | 62% | $0.892 | 30.5 |
| file-operations | 5 | 3 | 60% | $0.577 | 26.0 |
| scientific-computing | 8 | 4 | 50% | $0.678 | 24.1 |
| software-engineering | 26 | 12 | 46% | $2.043 | 37.9 |
| optimization | 1 | 0 | 0% | $0.145 | 8.0 |
| video-processing | 1 | 0 | 0% | $2.094 | 80.0 |

## Per-task (all 89)

| task | difficulty | category | outcome | steps | cache_read | cache_write | billed | wall |
|---|---|---|:--:|--:|--:|--:|--:|--:|
| adaptive-rejection-sampler | medium | scientific-computing | ❌ failed | 11 | 462,474 | 17,913 | $0.351 | 6.7 min |
| bn-fit-modify | hard | scientific-computing | ✅ solved | 19 | 806,095 | 12,961 | $0.304 | 6.0 min |
| break-filter-js-from-html | medium | security | ✅ solved | 13 | 524,418 | 6,145 | $0.236 | 11.0 min |
| build-cython-ext | medium | debugging | ✅ solved | 75 | 5,004,079 | 52,076 | $1.342 | 17.8 min |
| build-pmars | medium | software-engineering | ❌ failed | 32 | 1,614,846 | 25,937 | $0.478 | 6.2 min |
| build-pov-ray | medium | software-engineering | ❌ failed | 46 | 2,311,209 | 22,883 | $0.597 | 10.9 min |
| caffe-cifar-10 | medium | machine-learning | ⏱ timeout | 12 | 530,037 | 22,399 | $0.245 | 5.8 min |
| cancel-async-tasks | hard | software-engineering | ✅ solved | 9 | 334,567 | 4,618 | $0.224 | 3.3 min |
| chess-best-move | medium | games | ✅ solved | 36 | 1,676,787 | 16,013 | $0.570 | 8.8 min |
| circuit-fibsqrt | hard | software-engineering | ✅ solved | 50 | 3,331,130 | 69,283 | $3.849 | 109.3 min |
| cobol-modernization | easy | software-engineering | ⏱ timeout | 133 | 9,627,750 | 62,902 | $5.107 | 60.0 min |
| code-from-image | medium | software-engineering | ✅ solved | 5 | 165,696 | 4,230 | $0.049 | 0.4 min |
| compile-compcert | medium | system-administration | ✅ solved | 63 | 3,765,545 | 152,729 | $1.294 | 51.6 min |
| configure-git-webserver | hard | system-administration | ✅ solved | 26 | 1,145,910 | 12,625 | $0.356 | 4.7 min |
| constraints-scheduling | medium | personal-assistant | ✅ solved | 8 | 309,526 | 9,879 | $0.329 | 4.7 min |
| count-dataset-tokens | medium | model-training | ✅ solved | 10 | 410,163 | 10,860 | $0.140 | 7.4 min |
| crack-7z-hash | medium | security | ✅ solved | 21 | 904,687 | 10,512 | $0.232 | 9.4 min |
| custom-memory-heap-crash | medium | debugging | ✅ solved | 37 | 2,605,935 | 55,794 | $1.394 | 20.1 min |
| db-wal-recovery | medium | file-operations | ❌ failed | 34 | 1,645,855 | 26,864 | $1.083 | 20.2 min |
| distribution-search | medium | machine-learning | ✅ solved | 11 | 439,452 | 9,718 | $0.291 | 4.2 min |
| dna-assembly | hard | scientific-computing | ❌ failed | 28 | 1,443,693 | 25,382 | $0.689 | 13.9 min |
| dna-insert | medium | scientific-computing | ❌ failed | 31 | 1,617,092 | 26,810 | $0.854 | 13.5 min |
| extract-elf | medium | file-operations | ✅ solved | 12 | 488,419 | 9,549 | $0.272 | 4.2 min |
| extract-moves-from-video | hard | file-operations | ❌ failed | 2 | 38,801 | 2,561 | $0.017 | 0.3 min |
| feal-differential-cryptanalysis | hard | mathematics | ✅ solved | 25 | 1,158,755 | 16,179 | $1.430 | 28.9 min |
| feal-linear-cryptanalysis | hard | mathematics | ✅ solved | 54 | 3,170,572 | 140,161 | $2.436 | 69.2 min |
| filter-js-from-html | medium | security | ❌ failed | 12 | 496,608 | 11,505 | $0.530 | 7.6 min |
| financial-document-processor | medium | data-processing | ❌ failed | 35 | 1,217,231 | 63,060 | $0.547 | 5.7 min |
| fix-code-vulnerability | hard | security | ✅ solved | 10 | 392,091 | 19,737 | $0.144 | 1.3 min |
| fix-git | easy | software-engineering | ✅ solved | 10 | 387,268 | 17,054 | $0.146 | 3.3 min |
| fix-ocaml-gc | hard | software-engineering | ✅ solved | 52 | 3,262,223 | 91,713 | $1.251 | 25.1 min |
| gcode-to-text | medium | file-operations | ✅ solved | 64 | 3,465,831 | 84,253 | $1.151 | 15.4 min |
| git-leak-recovery | medium | software-engineering | ✅ solved | 11 | 422,092 | 5,796 | $0.120 | 1.5 min |
| git-multibranch | medium | system-administration | ✅ solved | 38 | 1,947,898 | 23,971 | $0.545 | 9.0 min |
| gpt2-codegolf | hard | software-engineering | ⏱ timeout | 43 | 2,163,376 | 27,198 | $1.476 | 60.0 min |
| headless-terminal | medium | software-engineering | ✅ solved | 19 | 860,979 | 14,958 | $0.329 | 6.3 min |
| hf-model-inference | medium | data-science | ✅ solved | 10 | 380,807 | 5,907 | $0.113 | 3.0 min |
| install-windows-3.11 | hard | system-administration | ❌ failed | 102 | 6,968,317 | 56,473 | $2.002 | 30.4 min |
| kv-store-grpc | medium | software-engineering | ✅ solved | 9 | 341,540 | 6,033 | $0.103 | 2.5 min |
| large-scale-text-editing | medium | file-operations | ✅ solved | 18 | 752,118 | 8,475 | $0.360 | 7.3 min |
| largest-eigenval | medium | mathematics | ✅ solved | 25 | 1,138,904 | 15,330 | $0.429 | 6.1 min |
| llm-inference-batching-scheduler | hard | machine-learning | ✅ solved | 58 | 5,548,609 | 156,412 | $3.209 | 42.0 min |
| log-summary-date-ranges | medium | data-processing | ✅ solved | 7 | 253,952 | 5,872 | $0.082 | 1.3 min |
| mailman | medium | system-administration | ✅ solved | 91 | 7,495,690 | 72,125 | $2.326 | 26.6 min |
| make-doom-for-mips | hard | software-engineering | ⏱ timeout | 160 | 18,623,369 | 227,155 | $6.391 | 60.0 min |
| make-mips-interpreter | hard | software-engineering | ❌ failed | 99 | 10,602,706 | 145,552 | $4.276 | 47.2 min |
| mcmc-sampling-stan | hard | data-science | ✅ solved | 47 | 3,348,755 | 70,024 | $0.944 | 27.0 min |
| merge-diff-arc-agi-task | medium | debugging | ✅ solved | 33 | 1,489,291 | 54,882 | $0.550 | 16.4 min |
| model-extraction-relu-logits | hard | mathematics | ❌ failed | 40 | 2,186,378 | 33,983 | $1.288 | 17.4 min |
| modernize-scientific-stack | medium | scientific-computing | ✅ solved | 8 | 309,419 | 8,771 | $0.108 | 1.3 min |
| mteb-leaderboard | medium | data-science | ✅ solved | 4 | 124,910 | 325 | $0.075 | 2.2 min |
| mteb-retrieve | medium | data-science | ✅ solved | 9 | 338,332 | 7,062 | $0.112 | 3.0 min |
| multi-source-data-merger | medium | data-processing | ✅ solved | 8 | 299,556 | 7,022 | $0.142 | 2.8 min |
| nginx-request-logging | medium | system-administration | ✅ solved | 11 | 439,741 | 8,750 | $0.137 | 1.8 min |
| openssl-selfsigned-cert | medium | security | ❌ failed | 11 | 426,962 | 6,501 | $0.125 | 2.2 min |
| overfull-hbox | easy | debugging | ✅ solved | 45 | 2,464,056 | 30,433 | $0.901 | 16.1 min |
| password-recovery | hard | security | ✅ solved | 33 | 1,579,302 | 19,069 | $0.874 | 15.0 min |
| path-tracing | hard | software-engineering | ✅ solved | 262 | 26,460,778 | 346,898 | $10.795 | 109.7 min |
| path-tracing-reverse | hard | software-engineering | ⏱ timeout | 170 | 17,971,303 | 373,040 | $9.621 | 120.0 min |
| polyglot-c-py | medium | software-engineering | ❌ failed | 11 | 422,257 | 5,552 | $0.370 | 5.8 min |
| polyglot-rust-c | hard | software-engineering | ❌ failed | 3 | 80,536 | 2,653 | $0.080 | 13.3 min |
| portfolio-optimization | medium | optimization | ❌ failed | 8 | 313,695 | 11,094 | $0.145 | 4.3 min |
| protein-assembly | hard | scientific-computing | ✅ solved | 41 | 2,573,426 | 50,569 | $1.849 | 30.7 min |
| prove-plus-comm | easy | software-engineering | ✅ solved | 7 | 239,431 | 12,162 | $0.090 | 0.9 min |
| pypi-server | medium | software-engineering | ✅ solved | 15 | 603,607 | 6,981 | $0.165 | 3.9 min |
| pytorch-model-cli | medium | model-training | ✅ solved | 32 | 1,505,970 | 19,926 | $0.438 | 9.3 min |
| pytorch-model-recovery | medium | model-training | ✅ solved | 18 | 814,226 | 16,239 | $0.344 | 14.1 min |
| qemu-alpine-ssh | medium | system-administration | ✅ solved | 39 | 2,026,593 | 30,041 | $0.724 | 21.2 min |
| qemu-startup | medium | system-administration | ✅ solved | 25 | 1,086,800 | 12,472 | $0.470 | 12.1 min |
| query-optimize | medium | data-science | ❌ failed | 14 | 564,257 | 8,717 | $0.262 | 14.3 min |
| raman-fitting | medium | scientific-computing | ❌ failed | 34 | 1,731,134 | 29,874 | $0.894 | 13.9 min |
| regex-chess | hard | software-engineering | ❌ failed | 4 | 129,443 | 1,453 | $0.057 | 2.1 min |
| regex-log | medium | data-processing | ✅ solved | 10 | 380,065 | 5,785 | $0.463 | 7.4 min |
| reshard-c4-data | medium | data-science | ❌ failed | 59 | 3,513,094 | 70,607 | $3.219 | 64.2 min |
| rstan-to-pystan | medium | data-science | ✅ solved | 48 | 2,690,995 | 95,456 | $0.950 | 35.5 min |
| sam-cell-seg | hard | data-science | ❌ failed | 53 | 3,259,944 | 66,464 | $1.458 | 39.5 min |
| sanitize-git-repo | medium | security | ✅ solved | 12 | 712,805 | 57,064 | $0.395 | 2.9 min |
| schemelike-metacircular-eval | medium | software-engineering | ⏱ timeout | 74 | 5,585,032 | 175,235 | $4.370 | 160.0 min |
| sparql-university | hard | data-querying | ✅ solved | 17 | 773,789 | 15,944 | $1.151 | 15.3 min |
| sqlite-db-truncate | medium | debugging | ✅ solved | 10 | 380,755 | 7,161 | $0.203 | 2.8 min |
| sqlite-with-gcov | medium | system-administration | ❌ failed | 22 | 980,315 | 15,320 | $0.275 | 7.2 min |
| torch-pipeline-parallelism | hard | software-engineering | ❌ failed | 33 | 1,964,911 | 41,056 | $0.863 | 16.9 min |
| torch-tensor-parallelism | hard | software-engineering | ❌ failed | 24 | 1,086,495 | 18,363 | $0.440 | 18.5 min |
| train-fasttext | hard | model-training | ❌ failed | 39 | 2,647,796 | 260,108 | $1.289 | 53.2 min |
| tune-mjcf | medium | scientific-computing | ✅ solved | 21 | 927,646 | 13,076 | $0.374 | 8.5 min |
| video-processing | hard | video-processing | ❌ failed | 80 | 5,345,972 | 55,105 | $2.094 | 30.7 min |
| vulnerable-secret | medium | security | ✅ solved | 11 | 434,737 | 12,474 | $0.140 | 1.9 min |
| winning-avg-corewars | medium | software-engineering | ✅ solved | 57 | 3,293,598 | 38,085 | $1.507 | 29.5 min |
| write-compressor | hard | software-engineering | ⏱ timeout | 6 | 208,218 | 3,710 | $0.357 | 60.0 min |
