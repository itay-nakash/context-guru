# Full results — rtk (Terminal-Bench 2.0, 89 tasks)

Full per-task results for the **rtk** arm on Terminal-Bench 2.0 (`claude-code` on `aws/claude-sonnet-5`, live). Same cache-aware cost model and 4× budget as the other arms. For the four-way analysis (cost decomposition, per-component, verdict) see the **[Terminal-Bench comparison](terminal-bench-comparison.md)**; the reference arm is the **[baseline](terminal-bench-baseline.md)**. See [REPRODUCE.md](REPRODUCE.md).

## Totals

| attempted | solved | solve rate | completed | timed out | total billed cost | mean steps* | cache-hit |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 89 | 55 | **61.8%** | 82 | 7 | $118.83 | 40.0 | 97.3% |

\* mean steps over the 82 completed tasks (timed-out runs are truncated). Solve rate over **completed-only** tasks: **55/82 = 67.1%**.

### Token & cost accounting (cache-aware, all 89 tasks)

| tier | tokens | $/M | billed |
|---|--:|--:|--:|
| cache-read (input) | 253,994,766 | 0.20 | $50.80 |
| cache-write (input) | 7,047,955 | 2.50 | $17.62 |
| fresh (input) | 55,363 | 2.00 | $0.11 |
| completion (output) | 5,029,700 | 10.00 | $50.30 |
| **total** | | | **$118.83** |

Cache-read is **43%** of the bill at a **97.3%** cache-hit rate — as on SWE-bench, a heavily-cached agent, so the lever a compaction layer must pull is cache-read tokens.

## Timeouts (7 long-horizon tasks)

These tasks still hit the wall-clock budget under the **extended 4×** timeout (up to ~4 h each) and scored **reward 0** — counted as failures in the solve rate above. A large part of the cause is **gateway latency, not only agent capability**: Terminal-Bench's timeouts assume a fast endpoint (~2–5 s/request), but this IBM LiteLLM gateway runs **~26 s/request** (5–10× slower), so long-horizon tasks that need many round-trips run out of clock (concurrency is *not* the cause — latency was flat ~23–30 s/req from n=1 to n=24). They are all `hard`/long software-engineering and compute tasks (path-tracing, a MIPS Doom port, a metacircular evaluator, COBOL modernization, GPT-2 code-golf, CIFAR training). A compaction arm that cuts round-trips could bring some under budget, so the timeout count is itself a comparison metric.

| task | difficulty | category | steps before timeout | partial billed | budget (4×) |
|---|---|---|--:|--:|--:|
| cobol-modernization | easy | software-engineering | 88 | $4.11 | 60 min |
| make-doom-for-mips | hard | software-engineering | 149 | $5.51 | 60 min |
| query-optimize | medium | data-science | 21 | $0.35 | 60 min |
| schemelike-metacircular-eval | medium | software-engineering | 71 | $5.57 | 160 min |
| torch-pipeline-parallelism | hard | software-engineering | 13 | $0.84 | 60 min |
| tune-mjcf | medium | scientific-computing | 26 | $0.50 | 60 min |
| write-compressor | hard | software-engineering | 7 | $0.52 | 60 min |

## By difficulty (all 89 tasks; timeouts = failures)

| difficulty | tasks | solved | rate | timed out | mean $/task |
|---|--:|--:|--:|--:|--:|
| easy | 4 | 3 | 75% | 1 | $1.470 |
| medium | 55 | 38 | 69% | 3 | $0.783 |
| hard | 30 | 14 | 47% | 3 | $2.329 |

## By category (all 89 tasks)

| category | tasks | solved | rate | mean $/task | mean steps* |
|---|--:|--:|--:|--:|--:|
| data-querying | 1 | 1 | 100% | $0.565 | 18.0 |
| debugging | 5 | 5 | 100% | $1.013 | 44.8 |
| optimization | 1 | 1 | 100% | $0.740 | 34.0 |
| personal-assistant | 1 | 1 | 100% | $0.363 | 7.0 |
| file-operations | 5 | 4 | 80% | $1.302 | 55.4 |
| data-processing | 4 | 3 | 75% | $0.321 | 14.8 |
| mathematics | 4 | 3 | 75% | $1.034 | 18.2 |
| security | 8 | 6 | 75% | $0.681 | 22.0 |
| machine-learning | 3 | 2 | 67% | $0.753 | 21.7 |
| system-administration | 9 | 6 | 67% | $1.263 | 56.0 |
| software-engineering | 26 | 15 | 58% | $1.961 | 44.0 |
| data-science | 8 | 4 | 50% | $1.345 | 51.6 |
| model-training | 4 | 2 | 50% | $2.558 | 61.5 |
| scientific-computing | 8 | 2 | 25% | $0.876 | 31.0 |
| games | 1 | 0 | 0% | $0.785 | 33.0 |
| video-processing | 1 | 0 | 0% | $1.333 | 61.0 |

## Per-task (all 89)

| task | difficulty | category | outcome | steps | cache_read | cache_write | billed | wall |
|---|---|---|:--:|--:|--:|--:|--:|--:|
| adaptive-rejection-sampler | medium | scientific-computing | ❌ failed | 22 | 1,051,805 | 22,713 | $0.697 | 19.1 min |
| bn-fit-modify | hard | scientific-computing | ✅ solved | 28 | 1,333,761 | 21,340 | $0.503 | 11.4 min |
| break-filter-js-from-html | medium | security | ✅ solved | 25 | 1,042,799 | 48,137 | $0.418 | 4.4 min |
| build-cython-ext | medium | debugging | ✅ solved | 69 | 4,517,952 | 48,120 | $1.217 | 18.3 min |
| build-pmars | medium | software-engineering | ✅ solved | 27 | 1,289,458 | 21,190 | $0.373 | 6.5 min |
| build-pov-ray | medium | software-engineering | ❌ failed | 39 | 2,052,249 | 28,593 | $0.568 | 7.9 min |
| caffe-cifar-10 | medium | machine-learning | ❌ failed | 15 | 339,775 | 58,234 | $0.413 | 14.4 min |
| cancel-async-tasks | hard | software-engineering | ❌ failed | 14 | 578,488 | 9,560 | $0.327 | 4.3 min |
| chess-best-move | medium | games | ❌ failed | 33 | 1,493,538 | 13,834 | $0.785 | 10.1 min |
| circuit-fibsqrt | hard | software-engineering | ✅ solved | 93 | 7,006,395 | 67,340 | $4.372 | 147.9 min |
| cobol-modernization | easy | software-engineering | ⏱ timeout | 88 | 5,909,121 | 58,247 | $4.109 | 60.0 min |
| code-from-image | medium | software-engineering | ✅ solved | 5 | 167,244 | 4,755 | $0.051 | 0.8 min |
| compile-compcert | medium | system-administration | ✅ solved | 63 | 3,509,261 | 155,507 | $1.265 | 75.2 min |
| configure-git-webserver | hard | system-administration | ❌ failed | 3 | 80,728 | 3,365 | $0.093 | 1.8 min |
| constraints-scheduling | medium | personal-assistant | ✅ solved | 7 | 263,567 | 7,389 | $0.363 | 5.1 min |
| count-dataset-tokens | medium | model-training | ❌ failed | 19 | 869,572 | 14,470 | $0.269 | 12.7 min |
| crack-7z-hash | medium | security | ✅ solved | 18 | 754,641 | 9,396 | $0.194 | 5.2 min |
| custom-memory-heap-crash | medium | debugging | ✅ solved | 37 | 2,130,474 | 35,044 | $1.575 | 22.9 min |
| db-wal-recovery | medium | file-operations | ✅ solved | 11 | 444,111 | 9,057 | $0.149 | 1.9 min |
| distribution-search | medium | machine-learning | ✅ solved | 9 | 354,059 | 10,033 | $0.284 | 4.0 min |
| dna-assembly | hard | scientific-computing | ❌ failed | 36 | 1,954,477 | 37,726 | $1.211 | 29.4 min |
| dna-insert | medium | scientific-computing | ❌ failed | 34 | 1,929,352 | 33,279 | $0.784 | 11.6 min |
| extract-elf | medium | file-operations | ✅ solved | 18 | 817,038 | 14,957 | $0.482 | 6.7 min |
| extract-moves-from-video | hard | file-operations | ❌ failed | 154 | 10,055,685 | 423,412 | $4.524 | 87.9 min |
| feal-differential-cryptanalysis | hard | mathematics | ✅ solved | 20 | 885,659 | 14,868 | $1.044 | 21.2 min |
| feal-linear-cryptanalysis | hard | mathematics | ✅ solved | 21 | 996,093 | 18,872 | $2.220 | 55.5 min |
| filter-js-from-html | medium | security | ❌ failed | 5 | 169,014 | 6,086 | $0.178 | 2.5 min |
| financial-document-processor | medium | data-processing | ❌ failed | 30 | 964,608 | 79,818 | $0.577 | 4.8 min |
| fix-code-vulnerability | hard | security | ✅ solved | 8 | 298,343 | 15,737 | $0.115 | 0.9 min |
| fix-git | easy | software-engineering | ✅ solved | 10 | 392,314 | 17,427 | $0.151 | 3.4 min |
| fix-ocaml-gc | hard | software-engineering | ✅ solved | 33 | 1,761,801 | 63,160 | $0.864 | 22.8 min |
| gcode-to-text | medium | file-operations | ✅ solved | 76 | 3,084,126 | 47,846 | $0.974 | 14.3 min |
| git-leak-recovery | medium | software-engineering | ✅ solved | 12 | 473,452 | 7,151 | $0.148 | 4.9 min |
| git-multibranch | medium | system-administration | ✅ solved | 33 | 1,584,684 | 20,825 | $0.466 | 4.4 min |
| gpt2-codegolf | hard | software-engineering | ❌ failed | 57 | 5,109,817 | 128,888 | $2.633 | 53.5 min |
| headless-terminal | medium | software-engineering | ✅ solved | 22 | 977,190 | 15,363 | $0.410 | 7.4 min |
| hf-model-inference | medium | data-science | ✅ solved | 7 | 254,085 | 5,762 | $0.085 | 1.9 min |
| install-windows-3.11 | hard | system-administration | ❌ failed | 41 | 2,296,949 | 31,986 | $0.680 | 7.1 min |
| kv-store-grpc | medium | software-engineering | ✅ solved | 12 | 480,425 | 7,239 | $0.139 | 2.1 min |
| large-scale-text-editing | medium | file-operations | ✅ solved | 18 | 761,039 | 9,874 | $0.379 | 8.5 min |
| largest-eigenval | medium | mathematics | ✅ solved | 18 | 771,732 | 11,251 | $0.355 | 5.2 min |
| llm-inference-batching-scheduler | hard | machine-learning | ✅ solved | 41 | 2,706,175 | 51,914 | $1.561 | 26.8 min |
| log-summary-date-ranges | medium | data-processing | ✅ solved | 8 | 302,756 | 6,793 | $0.106 | 2.7 min |
| mailman | medium | system-administration | ❌ failed | 163 | 17,616,302 | 117,016 | $5.423 | 56.5 min |
| make-doom-for-mips | hard | software-engineering | ⏱ timeout | 149 | 16,295,469 | 179,482 | $5.505 | 60.0 min |
| make-mips-interpreter | hard | software-engineering | ❌ failed | 177 | 19,611,308 | 201,572 | $6.868 | 72.0 min |
| mcmc-sampling-stan | hard | data-science | ✅ solved | 34 | 2,305,387 | 99,717 | $0.806 | 21.6 min |
| merge-diff-arc-agi-task | medium | debugging | ✅ solved | 31 | 1,482,844 | 17,610 | $0.562 | 10.7 min |
| model-extraction-relu-logits | hard | mathematics | ❌ failed | 14 | 608,883 | 15,478 | $0.519 | 8.3 min |
| modernize-scientific-stack | medium | scientific-computing | ✅ solved | 5 | 171,249 | 7,013 | $0.064 | 0.5 min |
| mteb-leaderboard | medium | data-science | ❌ failed | 134 | 9,863,579 | 565,731 | $4.007 | 109.2 min |
| mteb-retrieve | medium | data-science | ❌ failed | 6 | 210,717 | 5,444 | $0.067 | 1.4 min |
| multi-source-data-merger | medium | data-processing | ✅ solved | 8 | 302,525 | 7,448 | $0.137 | 2.1 min |
| nginx-request-logging | medium | system-administration | ✅ solved | 12 | 490,012 | 9,188 | $0.148 | 2.9 min |
| openssl-selfsigned-cert | medium | security | ❌ failed | 10 | 388,910 | 7,050 | $0.117 | 1.6 min |
| overfull-hbox | easy | debugging | ✅ solved | 77 | 4,616,543 | 34,611 | $1.537 | 17.2 min |
| password-recovery | hard | security | ✅ solved | 73 | 4,769,712 | 54,903 | $3.567 | 55.1 min |
| path-tracing | hard | software-engineering | ✅ solved | 194 | 18,483,361 | 205,361 | $7.453 | 108.5 min |
| path-tracing-reverse | hard | software-engineering | ✅ solved | 88 | 8,857,683 | 211,905 | $4.847 | 64.1 min |
| polyglot-c-py | medium | software-engineering | ❌ failed | 9 | 337,747 | 5,050 | $0.231 | 3.4 min |
| polyglot-rust-c | hard | software-engineering | ❌ failed | 12 | 471,616 | 5,765 | $0.746 | 13.1 min |
| portfolio-optimization | medium | optimization | ✅ solved | 34 | 1,844,659 | 29,258 | $0.740 | 24.4 min |
| protein-assembly | hard | scientific-computing | ❌ failed | 48 | 3,634,400 | 64,446 | $2.165 | 31.4 min |
| prove-plus-comm | easy | software-engineering | ✅ solved | 6 | 199,212 | 12,514 | $0.082 | 0.9 min |
| pypi-server | medium | software-engineering | ✅ solved | 13 | 518,027 | 6,917 | $0.140 | 2.3 min |
| pytorch-model-cli | medium | model-training | ✅ solved | 32 | 1,532,769 | 20,909 | $0.477 | 8.6 min |
| pytorch-model-recovery | medium | model-training | ✅ solved | 25 | 1,160,668 | 28,739 | $0.478 | 21.1 min |
| qemu-alpine-ssh | medium | system-administration | ✅ solved | 117 | 8,055,246 | 58,224 | $2.202 | 53.7 min |
| qemu-startup | medium | system-administration | ✅ solved | 26 | 1,206,220 | 16,004 | $0.461 | 7.8 min |
| query-optimize | medium | data-science | ⏱ timeout | 21 | 913,741 | 12,105 | $0.355 | 11.5 min |
| raman-fitting | medium | scientific-computing | ❌ failed | 44 | 2,511,383 | 39,190 | $1.080 | 15.1 min |
| regex-chess | hard | software-engineering | ✅ solved | 47 | 4,397,843 | 272,504 | $2.387 | 38.5 min |
| regex-log | medium | data-processing | ✅ solved | 13 | 528,827 | 9,122 | $0.465 | 6.8 min |
| reshard-c4-data | medium | data-science | ✅ solved | 19 | 884,423 | 16,405 | $0.933 | 18.4 min |
| rstan-to-pystan | medium | data-science | ✅ solved | 81 | 5,262,916 | 309,910 | $2.127 | 85.6 min |
| sam-cell-seg | hard | data-science | ❌ failed | 80 | 5,940,570 | 78,345 | $2.379 | 42.5 min |
| sanitize-git-repo | medium | security | ✅ solved | 20 | 1,441,011 | 70,680 | $0.578 | 4.2 min |
| schemelike-metacircular-eval | medium | software-engineering | ⏱ timeout | 71 | 5,486,995 | 91,141 | $5.573 | 160.0 min |
| sparql-university | hard | data-querying | ✅ solved | 18 | 830,088 | 14,463 | $0.565 | 18.0 min |
| sqlite-db-truncate | medium | debugging | ✅ solved | 10 | 382,532 | 7,665 | $0.174 | 3.5 min |
| sqlite-with-gcov | medium | system-administration | ✅ solved | 46 | 2,370,426 | 26,391 | $0.626 | 14.3 min |
| torch-pipeline-parallelism | hard | software-engineering | ⏱ timeout | 13 | 533,963 | 28,213 | $0.836 | 17.8 min |
| torch-tensor-parallelism | hard | software-engineering | ✅ solved | 8 | 304,302 | 6,430 | $0.230 | 3.3 min |
| train-fasttext | hard | model-training | ❌ failed | 170 | 14,587,640 | 2,255,472 | $9.010 | 232.9 min |
| tune-mjcf | medium | scientific-computing | ⏱ timeout | 26 | 1,199,162 | 15,669 | $0.500 | 10.1 min |
| video-processing | hard | video-processing | ❌ failed | 61 | 3,802,030 | 47,396 | $1.333 | 15.6 min |
| vulnerable-secret | medium | security | ✅ solved | 17 | 720,487 | 13,460 | $0.279 | 3.3 min |
| winning-avg-corewars | medium | software-engineering | ✅ solved | 46 | 2,386,296 | 25,057 | $1.416 | 30.5 min |
| write-compressor | hard | software-engineering | ⏱ timeout | 7 | 259,271 | 4,424 | $0.520 | 60.0 min |
