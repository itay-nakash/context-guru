# Full results — context-guru (codesmart) (Terminal-Bench 2.0, 89 tasks)

Full per-task results for the **context-guru (codesmart)** arm on Terminal-Bench 2.0 (`claude-code` on `aws/claude-sonnet-5`, live). Same cache-aware cost model and 4× budget as the other arms. For the four-way analysis (cost decomposition, per-component, verdict) see the **[Terminal-Bench comparison](terminal-bench-comparison.md)**; the reference arm is the **[baseline](terminal-bench-baseline.md)**. See [REPRODUCE.md](REPRODUCE.md).

## Totals

| attempted | solved | solve rate | completed | timed out | total billed cost | mean steps* | cache-hit |
|--:|--:|--:|--:|--:|--:|--:|--:|
| 89 | 58 | **65.2%** | 78 | 11 | $99.29 | 34.7 | 96.9% |

\* mean steps over the 78 completed tasks (timed-out runs are truncated). Solve rate over **completed-only** tasks: **57/78 = 73.1%**.

### Token & cost accounting (cache-aware, all 89 tasks)

| tier | tokens | $/M | billed |
|---|--:|--:|--:|
| cache-read (input) | 204,631,836 | 0.20 | $40.93 |
| cache-write (input) | 6,525,522 | 2.50 | $16.31 |
| fresh (input) | 107,482 | 2.00 | $0.21 |
| completion (output) | 4,183,424 | 10.00 | $41.83 |
| **total** | | | **$99.29** |

Cache-read is **41%** of the bill at a **96.9%** cache-hit rate — as on SWE-bench, a heavily-cached agent, so the lever a compaction layer must pull is cache-read tokens.

## Timeouts (11 long-horizon tasks)

These tasks still hit the wall-clock budget under the **extended 4×** timeout (up to ~4 h each) and scored **reward 0** — counted as failures in the solve rate above. A large part of the cause is **gateway latency, not only agent capability**: Terminal-Bench's timeouts assume a fast endpoint (~2–5 s/request), but this IBM LiteLLM gateway runs **~26 s/request** (5–10× slower), so long-horizon tasks that need many round-trips run out of clock (concurrency is *not* the cause — latency was flat ~23–30 s/req from n=1 to n=24). They are all `hard`/long software-engineering and compute tasks (path-tracing, a MIPS Doom port, a metacircular evaluator, COBOL modernization, GPT-2 code-golf, CIFAR training). A compaction arm that cuts round-trips could bring some under budget, so the timeout count is itself a comparison metric.

| task | difficulty | category | steps before timeout | partial billed | budget (4×) |
|---|---|---|--:|--:|--:|
| caffe-cifar-10 | medium | machine-learning | None | $0.00 | 80 min |
| cobol-modernization | easy | software-engineering | 110 | $4.40 | 60 min |
| gpt2-codegolf | hard | software-engineering | 28 | $1.13 | 60 min |
| make-doom-for-mips | hard | software-engineering | 141 | $5.76 | 60 min |
| mteb-retrieve | medium | data-science | None | $0.00 | 120 min |
| path-tracing-reverse | hard | software-engineering | 58 | $2.51 | 120 min |
| polyglot-rust-c | hard | software-engineering | 50 | $2.81 | 60 min |
| protein-assembly | hard | scientific-computing | 8 | $0.12 | 120 min |
| pytorch-model-recovery | medium | model-training | 21 | $0.44 | 60 min |
| schemelike-metacircular-eval | medium | software-engineering | 96 | $4.77 | 160 min |
| write-compressor | hard | software-engineering | 4 | $0.19 | 60 min |

## By difficulty (all 89 tasks; timeouts = failures)

| difficulty | tasks | solved | rate | timed out | mean $/task |
|---|--:|--:|--:|--:|--:|
| easy | 4 | 4 | 100% | 1 | $1.451 |
| medium | 55 | 41 | 75% | 4 | $0.781 |
| hard | 30 | 13 | 43% | 6 | $1.685 |

## By category (all 89 tasks)

| category | tasks | solved | rate | mean $/task | mean steps* |
|---|--:|--:|--:|--:|--:|
| data-processing | 4 | 4 | 100% | $0.226 | 11.5 |
| data-querying | 1 | 1 | 100% | $0.818 | 16.0 |
| debugging | 5 | 5 | 100% | $1.324 | 56.6 |
| optimization | 1 | 1 | 100% | $0.609 | 23.0 |
| personal-assistant | 1 | 1 | 100% | $0.229 | 6.0 |
| file-operations | 5 | 4 | 80% | $0.801 | 36.2 |
| mathematics | 4 | 3 | 75% | $0.813 | 15.5 |
| security | 8 | 6 | 75% | $0.740 | 25.8 |
| machine-learning | 3 | 2 | 67% | $0.739 | 29.5 |
| system-administration | 9 | 6 | 67% | $0.725 | 38.8 |
| data-science | 8 | 5 | 62% | $1.682 | 56.9 |
| software-engineering | 26 | 15 | 58% | $1.662 | 33.7 |
| model-training | 4 | 2 | 50% | $0.599 | 33.0 |
| scientific-computing | 8 | 3 | 38% | $0.596 | 26.9 |
| games | 1 | 0 | 0% | $0.386 | 17.0 |
| video-processing | 1 | 0 | 0% | $3.961 | 129.0 |

## Per-task (all 89)

| task | difficulty | category | outcome | steps | cache_read | cache_write | billed | wall |
|---|---|---|:--:|--:|--:|--:|--:|--:|
| adaptive-rejection-sampler | medium | scientific-computing | ❌ failed | 32 | 1,674,712 | 28,817 | $1.053 | 26.5 min |
| bn-fit-modify | hard | scientific-computing | ✅ solved | 23 | 1,010,421 | 13,731 | $0.382 | 7.2 min |
| break-filter-js-from-html | medium | security | ✅ solved | 56 | 2,925,793 | 26,244 | $1.603 | 34.7 min |
| build-cython-ext | medium | debugging | ✅ solved | 116 | 8,772,811 | 73,085 | $2.208 | 26.5 min |
| build-pmars | medium | software-engineering | ✅ solved | 38 | 1,922,393 | 24,994 | $0.530 | 10.5 min |
| build-pov-ray | medium | software-engineering | ❌ failed | 30 | 1,500,560 | 24,878 | $0.428 | 5.2 min |
| caffe-cifar-10 | medium | machine-learning | ⏱ timeout | None | 0 | 0 | $0.000 | — |
| cancel-async-tasks | hard | software-engineering | ❌ failed | 9 | 336,627 | 5,252 | $0.172 | 2.1 min |
| chess-best-move | medium | games | ❌ failed | 17 | 704,277 | 10,456 | $0.386 | 6.0 min |
| circuit-fibsqrt | hard | software-engineering | ✅ solved | 60 | 4,235,103 | 81,094 | $3.590 | 91.2 min |
| cobol-modernization | easy | software-engineering | ⏱ timeout | 110 | 7,425,465 | 59,332 | $4.395 | 60.0 min |
| code-from-image | medium | software-engineering | ✅ solved | 5 | 165,696 | 4,236 | $0.049 | 0.7 min |
| compile-compcert | medium | system-administration | ✅ solved | 69 | 3,870,275 | 180,987 | $1.425 | 74.0 min |
| configure-git-webserver | hard | system-administration | ✅ solved | 21 | 911,693 | 11,638 | $0.281 | 6.7 min |
| constraints-scheduling | medium | personal-assistant | ✅ solved | 6 | 215,662 | 6,897 | $0.229 | 3.5 min |
| count-dataset-tokens | medium | model-training | ✅ solved | 10 | 389,414 | 7,591 | $0.129 | 6.0 min |
| crack-7z-hash | medium | security | ✅ solved | 19 | 788,652 | 24,805 | $0.245 | 17.9 min |
| custom-memory-heap-crash | medium | debugging | ✅ solved | 75 | 5,344,207 | 56,082 | $2.507 | 36.1 min |
| db-wal-recovery | medium | file-operations | ✅ solved | 10 | 387,762 | 6,433 | $0.129 | 1.6 min |
| distribution-search | medium | machine-learning | ✅ solved | 21 | 958,627 | 16,830 | $0.654 | 11.5 min |
| dna-assembly | hard | scientific-computing | ❌ failed | 26 | 1,250,596 | 26,031 | $0.749 | 15.2 min |
| dna-insert | medium | scientific-computing | ❌ failed | 29 | 1,499,781 | 28,732 | $0.793 | 14.9 min |
| extract-elf | medium | file-operations | ✅ solved | 18 | 801,751 | 27,891 | $0.610 | 9.3 min |
| extract-moves-from-video | hard | file-operations | ❌ failed | 112 | 7,449,833 | 227,396 | $2.691 | 108.7 min |
| feal-differential-cryptanalysis | hard | mathematics | ✅ solved | 13 | 531,990 | 9,751 | $0.851 | 13.5 min |
| feal-linear-cryptanalysis | hard | mathematics | ✅ solved | 20 | 984,819 | 32,867 | $1.602 | 55.6 min |
| filter-js-from-html | medium | security | ❌ failed | 8 | 302,660 | 7,716 | $0.239 | 3.2 min |
| financial-document-processor | medium | data-processing | ✅ solved | 25 | 974,052 | 35,433 | $0.426 | 6.1 min |
| fix-code-vulnerability | hard | security | ✅ solved | 11 | 461,737 | 23,400 | $0.179 | 1.3 min |
| fix-git | easy | software-engineering | ✅ solved | 12 | 475,724 | 16,806 | $0.160 | 1.3 min |
| fix-ocaml-gc | hard | software-engineering | ✅ solved | 21 | 1,209,789 | 86,820 | $0.707 | 19.5 min |
| gcode-to-text | medium | file-operations | ✅ solved | 33 | 1,226,297 | 37,655 | $0.441 | 7.2 min |
| git-leak-recovery | medium | software-engineering | ✅ solved | 10 | 382,074 | 6,004 | $0.113 | 1.0 min |
| git-multibranch | medium | system-administration | ❌ failed | 37 | 1,860,897 | 21,763 | $0.581 | 5.6 min |
| gpt2-codegolf | hard | software-engineering | ⏱ timeout | 28 | 1,320,855 | 16,647 | $1.128 | 60.0 min |
| headless-terminal | medium | software-engineering | ✅ solved | 55 | 3,000,428 | 31,593 | $1.066 | 15.5 min |
| hf-model-inference | medium | data-science | ✅ solved | 10 | 383,229 | 6,288 | $0.119 | 2.4 min |
| install-windows-3.11 | hard | system-administration | ❌ failed | 49 | 2,592,143 | 29,114 | $0.729 | 8.1 min |
| kv-store-grpc | medium | software-engineering | ✅ solved | 11 | 429,312 | 6,723 | $0.126 | 1.3 min |
| large-scale-text-editing | medium | file-operations | ✅ solved | 8 | 307,912 | 7,638 | $0.135 | 3.2 min |
| largest-eigenval | medium | mathematics | ✅ solved | 20 | 881,834 | 13,592 | $0.333 | 4.0 min |
| llm-inference-batching-scheduler | hard | machine-learning | ✅ solved | 38 | 2,496,481 | 54,992 | $1.562 | 27.8 min |
| log-summary-date-ranges | medium | data-processing | ✅ solved | 6 | 210,077 | 5,648 | $0.071 | 0.6 min |
| mailman | medium | system-administration | ❌ failed | 81 | 6,947,541 | 75,092 | $2.097 | 23.8 min |
| make-doom-for-mips | hard | software-engineering | ⏱ timeout | 141 | 13,556,629 | 354,083 | $5.760 | 60.0 min |
| make-mips-interpreter | hard | software-engineering | ✅ solved | 171 | 16,819,007 | 906,773 | $7.554 | 71.4 min |
| mcmc-sampling-stan | hard | data-science | ✅ solved | 44 | 2,662,323 | 51,716 | $0.790 | 22.6 min |
| merge-diff-arc-agi-task | medium | debugging | ✅ solved | 25 | 1,114,428 | 13,596 | $0.406 | 4.6 min |
| model-extraction-relu-logits | hard | mathematics | ❌ failed | 9 | 357,509 | 13,370 | $0.467 | 7.3 min |
| modernize-scientific-stack | medium | scientific-computing | ✅ solved | 6 | 215,145 | 6,818 | $0.083 | 0.7 min |
| mteb-leaderboard | medium | data-science | ✅ solved | 147 | 12,277,618 | 1,122,303 | $6.045 | 73.5 min |
| mteb-retrieve | medium | data-science | ⏱ timeout | None | 0 | 0 | $0.000 | — |
| multi-source-data-merger | medium | data-processing | ✅ solved | 7 | 256,234 | 6,751 | $0.103 | 3.1 min |
| nginx-request-logging | medium | system-administration | ✅ solved | 12 | 477,493 | 7,266 | $0.143 | 2.1 min |
| openssl-selfsigned-cert | medium | security | ✅ solved | 10 | 383,978 | 6,629 | $0.122 | 1.9 min |
| overfull-hbox | easy | debugging | ✅ solved | 53 | 3,388,975 | 42,344 | $1.168 | 13.1 min |
| password-recovery | hard | security | ❌ failed | 69 | 3,995,130 | 40,768 | $2.480 | 39.3 min |
| path-tracing | hard | software-engineering | ✅ solved | 114 | 9,509,036 | 442,884 | $4.820 | 77.1 min |
| path-tracing-reverse | hard | software-engineering | ⏱ timeout | 58 | 4,628,243 | 258,646 | $2.507 | 34.2 min |
| polyglot-c-py | medium | software-engineering | ❌ failed | 12 | 464,910 | 5,731 | $0.360 | 6.3 min |
| polyglot-rust-c | hard | software-engineering | ⏱ timeout | 50 | 2,642,242 | 30,164 | $2.810 | 60.0 min |
| portfolio-optimization | medium | optimization | ✅ solved | 23 | 1,127,622 | 22,185 | $0.609 | 11.5 min |
| protein-assembly | hard | scientific-computing | ⏱ timeout | 8 | 269,038 | 4,867 | $0.116 | 2.4 min |
| prove-plus-comm | easy | software-engineering | ✅ solved | 6 | 197,256 | 12,101 | $0.079 | 0.7 min |
| pypi-server | medium | software-engineering | ✅ solved | 16 | 653,138 | 7,711 | $0.174 | 8.0 min |
| pytorch-model-cli | medium | model-training | ✅ solved | 25 | 1,124,334 | 57,946 | $0.461 | 5.5 min |
| pytorch-model-recovery | medium | model-training | ⏱ timeout | 21 | 943,457 | 14,990 | $0.437 | 8.6 min |
| qemu-alpine-ssh | medium | system-administration | ✅ solved | 37 | 1,717,574 | 21,608 | $0.579 | 8.0 min |
| qemu-startup | medium | system-administration | ✅ solved | 24 | 1,024,044 | 9,915 | $0.459 | 7.7 min |
| query-optimize | medium | data-science | ❌ failed | 44 | 2,213,453 | 25,584 | $0.877 | 30.2 min |
| raman-fitting | medium | scientific-computing | ❌ failed | 48 | 2,626,488 | 36,884 | $1.164 | 19.3 min |
| regex-chess | hard | software-engineering | ❌ failed | 2 | 41,954 | 0 | $0.031 | 1.3 min |
| regex-log | medium | data-processing | ✅ solved | 8 | 301,024 | 9,175 | $0.303 | 4.3 min |
| reshard-c4-data | medium | data-science | ✅ solved | 25 | 1,264,046 | 22,514 | $1.226 | 19.9 min |
| rstan-to-pystan | medium | data-science | ✅ solved | 73 | 4,191,501 | 588,048 | $2.717 | 118.7 min |
| sam-cell-seg | hard | data-science | ❌ failed | 55 | 3,065,248 | 37,729 | $1.683 | 29.0 min |
| sanitize-git-repo | medium | security | ✅ solved | 22 | 1,591,443 | 186,042 | $0.893 | 7.4 min |
| schemelike-metacircular-eval | medium | software-engineering | ⏱ timeout | 96 | 6,646,069 | 120,462 | $4.768 | 160.0 min |
| sparql-university | hard | data-querying | ✅ solved | 16 | 734,172 | 13,592 | $0.818 | 12.5 min |
| sqlite-db-truncate | medium | debugging | ✅ solved | 14 | 576,165 | 11,206 | $0.332 | 5.1 min |
| sqlite-with-gcov | medium | system-administration | ✅ solved | 19 | 827,372 | 13,175 | $0.236 | 4.8 min |
| torch-pipeline-parallelism | hard | software-engineering | ✅ solved | 8 | 293,023 | 16,998 | $0.278 | 4.4 min |
| torch-tensor-parallelism | hard | software-engineering | ❌ failed | 10 | 405,241 | 12,627 | $0.290 | 4.9 min |
| train-fasttext | hard | model-training | ❌ failed | 64 | 3,191,706 | 210,917 | $1.371 | 113.6 min |
| tune-mjcf | medium | scientific-computing | ✅ solved | 24 | 1,086,103 | 13,996 | $0.426 | 10.7 min |
| video-processing | hard | video-processing | ❌ failed | 129 | 10,928,251 | 200,113 | $3.961 | 69.7 min |
| vulnerable-secret | medium | security | ✅ solved | 11 | 439,504 | 12,575 | $0.162 | 2.2 min |
| winning-avg-corewars | medium | software-engineering | ✅ solved | 51 | 2,757,923 | 30,691 | $1.125 | 15.8 min |
| write-compressor | hard | software-engineering | ⏱ timeout | 4 | 123,825 | 3,055 | $0.194 | 60.0 min |
