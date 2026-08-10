# Full results — headroom (hd-cache) (Terminal-Bench 2.0, 89 tasks)

Full per-task results for the **headroom (hd-cache)** arm on Terminal-Bench 2.0 (`claude-code` on `aws/claude-sonnet-5`, live). Same cache-aware cost model and 4× budget as the other arms. For the four-way analysis (cost decomposition, per-component, verdict) see the **[Terminal-Bench comparison](terminal-bench-comparison.md)**; the reference arm is the **[baseline](terminal-bench-baseline.md)**. See [REPRODUCE.md](REPRODUCE.md).

## Totals

| attempted | solved | solve rate | completed | timed out | total billed cost | mean steps* | cache-hit |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 89 | 64 | **71.9%** | 78 | 11 | $114.75 | 32.0 | 94.1% |

\* mean steps over the 78 completed tasks (timed-out runs are truncated). Solve rate over **completed-only** tasks: **64/78 = 82.1%**.

### Token & cost accounting (cache-aware, all 89 tasks)

| tier | tokens | $/M | billed |
|---|--:|--:|--:|
| cache-read (input) | 198,020,429 | 0.20 | $39.60 |
| cache-write (input) | 12,369,660 | 2.50 | $30.92 |
| fresh (input) | 97,763 | 2.00 | $0.20 |
| completion (output) | 4,402,777 | 10.00 | $44.03 |
| **total** | | | **$114.75** |

Cache-read is **35%** of the bill at a **94.1%** cache-hit rate — as on SWE-bench, a heavily-cached agent, so the lever a compaction layer must pull is cache-read tokens.

## Timeouts (11 long-horizon tasks)

These tasks still hit the wall-clock budget under the **extended 4×** timeout (up to ~4 h each) and scored **reward 0** — counted as failures in the solve rate above. A large part of the cause is **gateway latency, not only agent capability**: Terminal-Bench's timeouts assume a fast endpoint (~2–5 s/request), but this IBM LiteLLM gateway runs **~26 s/request** (5–10× slower), so long-horizon tasks that need many round-trips run out of clock (concurrency is *not* the cause — latency was flat ~23–30 s/req from n=1 to n=24). They are all `hard`/long software-engineering and compute tasks (path-tracing, a MIPS Doom port, a metacircular evaluator, COBOL modernization, GPT-2 code-golf, CIFAR training). A compaction arm that cuts round-trips could bring some under budget, so the timeout count is itself a comparison metric.

| task | difficulty | category | steps before timeout | partial billed | budget (4×) |
|---|---|---|--:|--:|--:|
| caffe-cifar-10 | medium | machine-learning | 38 | $0.77 | 80 min |
| cobol-modernization | easy | software-engineering | 69 | $2.35 | 60 min |
| extract-moves-from-video | hard | file-operations | 160 | $24.12 | 120 min |
| make-doom-for-mips | hard | software-engineering | 101 | $4.24 | 60 min |
| path-tracing | hard | software-engineering | 142 | $6.16 | 120 min |
| protein-assembly | hard | scientific-computing | 2 | $0.00 | 120 min |
| pytorch-model-cli | medium | model-training | 35 | $0.47 | 60 min |
| query-optimize | medium | data-science | 19 | $0.31 | 60 min |
| schemelike-metacircular-eval | medium | software-engineering | 77 | $4.66 | 160 min |
| tune-mjcf | medium | scientific-computing | 25 | $0.42 | 60 min |
| write-compressor | hard | software-engineering | 5 | $0.47 | 60 min |

## By difficulty (all 89 tasks; timeouts = failures)

| difficulty | tasks | solved | rate | timed out | mean $/task |
|---|--:|--:|--:|--:|--:|
| easy | 4 | 3 | 75% | 1 | $0.878 |
| medium | 55 | 42 | 76% | 5 | $0.631 |
| hard | 30 | 19 | 63% | 5 | $2.551 |

## By category (all 89 tasks)

| category | tasks | solved | rate | mean $/task | mean steps* |
|---|--:|--:|--:|--:|--:|
| data-querying | 1 | 1 | 100% | $0.351 | 8.0 |
| debugging | 5 | 5 | 100% | $0.909 | 42.8 |
| games | 1 | 1 | 100% | $0.383 | 20.0 |
| mathematics | 4 | 4 | 100% | $0.703 | 10.8 |
| optimization | 1 | 1 | 100% | $0.464 | 19.0 |
| personal-assistant | 1 | 1 | 100% | $0.244 | 6.0 |
| video-processing | 1 | 1 | 100% | $3.080 | 90.0 |
| system-administration | 9 | 8 | 89% | $0.933 | 47.2 |
| data-processing | 4 | 3 | 75% | $0.212 | 13.8 |
| data-science | 8 | 6 | 75% | $1.105 | 49.7 |
| security | 8 | 6 | 75% | $0.371 | 19.6 |
| machine-learning | 3 | 2 | 67% | $1.060 | 28.0 |
| software-engineering | 26 | 17 | 65% | $1.696 | 34.0 |
| file-operations | 5 | 3 | 60% | $5.065 | 15.8 |
| model-training | 4 | 2 | 50% | $0.801 | 33.3 |
| scientific-computing | 8 | 3 | 38% | $0.752 | 30.3 |

## Per-task (all 89)

| task | difficulty | category | outcome | steps | cache_read | cache_write | billed | wall |
|---|---|---|:--:|--:|--:|--:|--:|--:|
| adaptive-rejection-sampler | medium | scientific-computing | ❌ failed | 30 | 1,438,504 | 52,222 | $1.253 | 26.4 min |
| bn-fit-modify | hard | scientific-computing | ✅ solved | 22 | 928,502 | 13,410 | $0.375 | 7.2 min |
| break-filter-js-from-html | medium | security | ✅ solved | 35 | 1,600,416 | 17,679 | $0.879 | 21.7 min |
| build-cython-ext | medium | debugging | ✅ solved | 62 | 4,496,202 | 63,447 | $1.296 | 14.0 min |
| build-pmars | medium | software-engineering | ✅ solved | 39 | 1,926,499 | 29,817 | $0.560 | 7.3 min |
| build-pov-ray | medium | software-engineering | ✅ solved | 36 | 1,752,704 | 28,039 | $0.517 | 11.2 min |
| caffe-cifar-10 | medium | machine-learning | ⏱ timeout | 38 | 1,748,587 | 131,625 | $0.769 | 57.0 min |
| cancel-async-tasks | hard | software-engineering | ✅ solved | 8 | 279,969 | 5,770 | $0.200 | 2.5 min |
| chess-best-move | medium | games | ✅ solved | 20 | 806,656 | 9,186 | $0.383 | 4.9 min |
| circuit-fibsqrt | hard | software-engineering | ✅ solved | 53 | 3,341,016 | 46,604 | $2.881 | 178.9 min |
| cobol-modernization | easy | software-engineering | ⏱ timeout | 69 | 3,877,615 | 36,673 | $2.353 | 60.0 min |
| code-from-image | medium | software-engineering | ✅ solved | 5 | 156,544 | 4,231 | $0.047 | 0.5 min |
| compile-compcert | medium | system-administration | ✅ solved | 85 | 5,515,699 | 227,890 | $1.922 | 78.9 min |
| configure-git-webserver | hard | system-administration | ✅ solved | 13 | 494,249 | 7,317 | $0.166 | 7.2 min |
| constraints-scheduling | medium | personal-assistant | ✅ solved | 6 | 204,738 | 8,481 | $0.244 | 3.2 min |
| count-dataset-tokens | medium | model-training | ✅ solved | 13 | 526,046 | 17,052 | $0.196 | 5.3 min |
| crack-7z-hash | medium | security | ✅ solved | 20 | 814,384 | 11,301 | $0.221 | 6.3 min |
| custom-memory-heap-crash | medium | debugging | ✅ solved | 52 | 3,359,686 | 49,983 | $1.714 | 30.4 min |
| db-wal-recovery | medium | file-operations | ❌ failed | 17 | 689,904 | 13,305 | $0.408 | 5.6 min |
| distribution-search | medium | machine-learning | ✅ solved | 16 | 663,356 | 14,575 | $0.538 | 7.5 min |
| dna-assembly | hard | scientific-computing | ✅ solved | 56 | 3,250,476 | 53,252 | $2.386 | 43.6 min |
| dna-insert | medium | scientific-computing | ❌ failed | 18 | 773,481 | 13,546 | $0.320 | 5.5 min |
| extract-elf | medium | file-operations | ✅ solved | 18 | 763,108 | 13,337 | $0.390 | 5.2 min |
| extract-moves-from-video | hard | file-operations | ⏱ timeout | 160 | 7,363,596 | 7,806,411 | $24.118 | 120.0 min |
| feal-differential-cryptanalysis | hard | mathematics | ✅ solved | 13 | 502,491 | 9,262 | $1.003 | 16.9 min |
| feal-linear-cryptanalysis | hard | mathematics | ✅ solved | 13 | 532,046 | 12,513 | $1.316 | 45.3 min |
| filter-js-from-html | medium | security | ❌ failed | 22 | 989,347 | 16,881 | $0.496 | 5.7 min |
| financial-document-processor | medium | data-processing | ❌ failed | 26 | 533,975 | 61,704 | $0.360 | 2.5 min |
| fix-code-vulnerability | hard | security | ✅ solved | 16 | 633,232 | 20,508 | $0.206 | 1.5 min |
| fix-git | easy | software-engineering | ✅ solved | 11 | 412,077 | 17,470 | $0.151 | 1.0 min |
| fix-ocaml-gc | hard | software-engineering | ✅ solved | 27 | 1,340,872 | 145,817 | $0.778 | 27.9 min |
| gcode-to-text | medium | file-operations | ✅ solved | 17 | 692,582 | 11,878 | $0.227 | 4.1 min |
| git-leak-recovery | medium | software-engineering | ✅ solved | 16 | 611,482 | 7,048 | $0.172 | 1.5 min |
| git-multibranch | medium | system-administration | ✅ solved | 33 | 1,485,333 | 16,544 | $0.470 | 4.5 min |
| gpt2-codegolf | hard | software-engineering | ❌ failed | 2 | 0 | 39,156 | $0.138 | 0.9 min |
| headless-terminal | medium | software-engineering | ✅ solved | 16 | 652,521 | 15,294 | $0.289 | 13.3 min |
| hf-model-inference | medium | data-science | ✅ solved | 12 | 448,133 | 12,428 | $0.149 | 11.6 min |
| install-windows-3.11 | hard | system-administration | ❌ failed | 89 | 6,294,323 | 136,137 | $2.271 | 40.8 min |
| kv-store-grpc | medium | software-engineering | ✅ solved | 11 | 407,203 | 6,197 | $0.121 | 1.8 min |
| large-scale-text-editing | medium | file-operations | ✅ solved | 11 | 406,201 | 6,239 | $0.180 | 3.3 min |
| largest-eigenval | medium | mathematics | ✅ solved | 10 | 370,777 | 8,085 | $0.168 | 2.8 min |
| llm-inference-batching-scheduler | hard | machine-learning | ✅ solved | 40 | 2,384,925 | 52,778 | $1.873 | 28.7 min |
| log-summary-date-ranges | medium | data-processing | ✅ solved | 8 | 280,917 | 5,643 | $0.086 | 0.6 min |
| mailman | medium | system-administration | ✅ solved | 71 | 5,039,376 | 54,115 | $1.495 | 15.4 min |
| make-doom-for-mips | hard | software-engineering | ⏱ timeout | 101 | 11,022,228 | 111,639 | $4.237 | 60.0 min |
| make-mips-interpreter | hard | software-engineering | ❌ failed | 181 | 18,796,281 | 222,145 | $7.696 | 83.6 min |
| mcmc-sampling-stan | hard | data-science | ✅ solved | 39 | 2,322,900 | 54,635 | $0.702 | 20.2 min |
| merge-diff-arc-agi-task | medium | debugging | ✅ solved | 37 | 1,639,043 | 15,823 | $0.472 | 6.6 min |
| model-extraction-relu-logits | hard | mathematics | ✅ solved | 7 | 240,960 | 7,470 | $0.325 | 5.7 min |
| modernize-scientific-stack | medium | scientific-computing | ✅ solved | 6 | 202,237 | 6,055 | $0.067 | 0.6 min |
| mteb-leaderboard | medium | data-science | ✅ solved | 91 | 6,713,015 | 81,960 | $2.041 | 55.8 min |
| mteb-retrieve | medium | data-science | ✅ solved | 15 | 591,826 | 9,506 | $0.184 | 8.3 min |
| multi-source-data-merger | medium | data-processing | ✅ solved | 9 | 328,735 | 7,246 | $0.130 | 1.5 min |
| nginx-request-logging | medium | system-administration | ✅ solved | 12 | 458,387 | 9,220 | $0.144 | 2.4 min |
| openssl-selfsigned-cert | medium | security | ✅ solved | 16 | 620,007 | 8,407 | $0.184 | 1.6 min |
| overfull-hbox | easy | debugging | ✅ solved | 55 | 2,900,215 | 28,336 | $0.918 | 19.9 min |
| password-recovery | hard | security | ✅ solved | 24 | 1,018,613 | 12,598 | $0.475 | 6.0 min |
| path-tracing | hard | software-engineering | ⏱ timeout | 142 | 13,062,719 | 184,070 | $6.155 | 120.0 min |
| path-tracing-reverse | hard | software-engineering | ✅ solved | 112 | 11,500,154 | 209,909 | $6.088 | 102.2 min |
| polyglot-c-py | medium | software-engineering | ❌ failed | 11 | 400,585 | 5,841 | $0.396 | 8.5 min |
| polyglot-rust-c | hard | software-engineering | ✅ solved | 7 | 390,788 | 39,597 | $0.573 | 11.7 min |
| portfolio-optimization | medium | optimization | ✅ solved | 19 | 859,707 | 20,442 | $0.464 | 8.9 min |
| protein-assembly | hard | scientific-computing | ⏱ timeout | 2 | 0 | 0 | $0.000 | 0.1 min |
| prove-plus-comm | easy | software-engineering | ✅ solved | 7 | 225,644 | 12,207 | $0.091 | 5.8 min |
| pypi-server | medium | software-engineering | ✅ solved | 15 | 571,478 | 7,504 | $0.160 | 1.9 min |
| pytorch-model-cli | medium | model-training | ⏱ timeout | 35 | 1,595,679 | 20,587 | $0.471 | 11.1 min |
| pytorch-model-recovery | medium | model-training | ✅ solved | 19 | 844,676 | 18,634 | $0.377 | 12.7 min |
| qemu-alpine-ssh | medium | system-administration | ✅ solved | 79 | 4,459,313 | 41,132 | $1.345 | 39.3 min |
| qemu-startup | medium | system-administration | ✅ solved | 21 | 832,903 | 9,683 | $0.324 | 10.1 min |
| query-optimize | medium | data-science | ⏱ timeout | 19 | 757,494 | 15,322 | $0.307 | 10.2 min |
| raman-fitting | medium | scientific-computing | ❌ failed | 50 | 2,706,005 | 43,519 | $1.196 | 15.4 min |
| regex-chess | hard | software-engineering | ✅ solved | 75 | 6,457,203 | 482,484 | $3.309 | 79.9 min |
| regex-log | medium | data-processing | ✅ solved | 12 | 450,419 | 8,037 | $0.270 | 3.9 min |
| reshard-c4-data | medium | data-science | ✅ solved | 33 | 1,497,470 | 15,842 | $0.924 | 16.5 min |
| rstan-to-pystan | medium | data-science | ✅ solved | 88 | 5,680,153 | 442,469 | $2.661 | 106.6 min |
| sam-cell-seg | hard | data-science | ❌ failed | 70 | 4,414,374 | 60,442 | $1.873 | 35.5 min |
| sanitize-git-repo | medium | security | ❌ failed | 8 | 403,241 | 45,268 | $0.235 | 1.1 min |
| schemelike-metacircular-eval | medium | software-engineering | ⏱ timeout | 77 | 5,741,094 | 99,381 | $4.664 | 160.0 min |
| sparql-university | hard | data-querying | ✅ solved | 8 | 301,163 | 10,650 | $0.351 | 4.2 min |
| sqlite-db-truncate | medium | debugging | ✅ solved | 8 | 277,629 | 6,865 | $0.146 | 1.6 min |
| sqlite-with-gcov | medium | system-administration | ✅ solved | 22 | 924,943 | 16,688 | $0.264 | 8.7 min |
| torch-pipeline-parallelism | hard | software-engineering | ✅ solved | 41 | 2,156,546 | 25,746 | $0.994 | 35.2 min |
| torch-tensor-parallelism | hard | software-engineering | ❌ failed | 12 | 469,160 | 11,589 | $0.337 | 8.7 min |
| train-fasttext | hard | model-training | ❌ failed | 68 | 3,683,278 | 478,817 | $2.159 | 136.9 min |
| tune-mjcf | medium | scientific-computing | ⏱ timeout | 25 | 1,121,523 | 17,848 | $0.420 | 13.2 min |
| video-processing | hard | video-processing | ✅ solved | 90 | 6,490,786 | 73,613 | $3.080 | 70.4 min |
| vulnerable-secret | medium | security | ✅ solved | 16 | 654,879 | 15,453 | $0.270 | 2.8 min |
| winning-avg-corewars | medium | software-engineering | ✅ solved | 28 | 1,288,502 | 20,289 | $0.717 | 11.7 min |
| write-compressor | hard | software-engineering | ⏱ timeout | 5 | 156,724 | 3,842 | $0.471 | 60.0 min |
