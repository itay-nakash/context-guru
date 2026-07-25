# Full results — baseline (off) (SWE-bench Verified, 50 tasks)

Live through the harness, `claude-code` agent on `aws/claude-sonnet-5`. Cache-aware billed input cost (fresh $2/M · cache-read $0.20/M · cache-write $2.50/M) + output $10/M, recomputed from each trial's token tiers. See [REPRODUCE.md](REPRODUCE.md).

## Totals

| tasks scored | solved | rate | total billed cost | mean steps | cache-hit | agent wall (sum) |
|---|---|---|---|---|---|---|
| 50 | 43 | 86% | $31.98 | 36.1 | 98.1% | 317 min |

## Per-task

| task | reward | steps | cache_read | cache_write | billed cost |
|---|---|---|---|---|---|
| astropy__astropy-12907 | 1 | 28 | 1,486,182 | 39,072 | $0.443 |
| astropy__astropy-14365 | 1 | 26 | 1,331,718 | 30,002 | $0.428 |
| astropy__astropy-8707 | 0 | 26 | 1,287,520 | 30,970 | $0.391 |
| django__django-11095 | 1 | 31 | 1,446,457 | 28,197 | $0.422 |
| django__django-11211 | 1 | 57 | 3,336,980 | 75,407 | $0.999 |
| django__django-11477 | 1 | 56 | 3,023,951 | 38,688 | $0.881 |
| django__django-11790 | 1 | 25 | 1,131,935 | 24,533 | $0.337 |
| django__django-12050 | 1 | 7 | 241,379 | 13,657 | $0.090 |
| django__django-12308 | 1 | 25 | 1,169,638 | 22,221 | $0.347 |
| django__django-12858 | 0 | 42 | 2,212,083 | 39,423 | $0.789 |
| django__django-13128 | 1 | 123 | 10,152,118 | 212,282 | $2.985 |
| django__django-13363 | 1 | 45 | 2,586,634 | 45,535 | $0.759 |
| django__django-13568 | 1 | 32 | 1,522,384 | 26,424 | $0.438 |
| django__django-13810 | 1 | 31 | 1,783,070 | 44,681 | $0.780 |
| django__django-14034 | 1 | 84 | 5,832,079 | 59,733 | $1.794 |
| django__django-14349 | 1 | 14 | 593,392 | 22,577 | $0.216 |
| django__django-14559 | 1 | 25 | 1,164,866 | 29,554 | $0.352 |
| django__django-14792 | 0 | 69 | 4,396,102 | 52,915 | $1.184 |
| django__django-15128 | 1 | 59 | 3,602,051 | 45,027 | $0.988 |
| django__django-15380 | 1 | 21 | 966,153 | 24,392 | $0.295 |
| django__django-15572 | 1 | 10 | 385,788 | 17,396 | $0.139 |
| django__django-15930 | 1 | 34 | 1,716,231 | 29,791 | $0.533 |
| django__django-16145 | 1 | 13 | 516,372 | 16,664 | $0.165 |
| django__django-16502 | 0 | 42 | 2,120,703 | 32,030 | $0.645 |
| django__django-16667 | 0 | 22 | 971,978 | 22,685 | $0.285 |
| django__django-17087 | 1 | 11 | 420,513 | 14,777 | $0.144 |
| matplotlib__matplotlib-22719 | 1 | 27 | 1,315,560 | 27,939 | $0.395 |
| matplotlib__matplotlib-24570 | 1 | 20 | 892,408 | 25,106 | $0.322 |
| matplotlib__matplotlib-25775 | 1 | 108 | 8,921,304 | 79,696 | $2.271 |
| psf__requests-1142 | 1 | 13 | 521,493 | 19,474 | $0.181 |
| pydata__xarray-3151 | 1 | 22 | 1,004,803 | 24,540 | $0.318 |
| pydata__xarray-4966 | 1 | 30 | 1,472,856 | 28,726 | $0.453 |
| pylint-dev__pylint-4551 | 1 | 72 | 5,482,438 | 75,674 | $1.682 |
| pytest-dev__pytest-10051 | 1 | 23 | 1,003,515 | 19,024 | $0.289 |
| pytest-dev__pytest-7205 | 1 | 49 | 2,650,728 | 36,370 | $0.717 |
| scikit-learn__scikit-learn-10844 | 1 | 15 | 620,269 | 17,574 | $0.193 |
| scikit-learn__scikit-learn-13328 | 1 | 17 | 722,414 | 20,964 | $0.226 |
| scikit-learn__scikit-learn-14894 | 1 | 21 | 915,577 | 20,958 | $0.289 |
| scikit-learn__scikit-learn-9288 | 1 | 13 | 547,195 | 20,357 | $0.197 |
| sphinx-doc__sphinx-7454 | 1 | 22 | 1,028,492 | 25,064 | $0.314 |
| sphinx-doc__sphinx-8120 | 1 | 32 | 1,588,891 | 28,644 | $0.464 |
| sphinx-doc__sphinx-8638 | 1 | 71 | 3,217,605 | 82,483 | $1.141 |
| sphinx-doc__sphinx-9602 | 1 | 32 | 1,738,898 | 36,151 | $0.526 |
| sympy__sympy-13031 | 1 | 67 | 3,977,995 | 45,516 | $1.234 |
| sympy__sympy-13877 | 1 | 12 | 497,367 | 20,055 | $0.174 |
| sympy__sympy-15599 | 1 | 37 | 1,773,674 | 30,519 | $0.598 |
| sympy__sympy-17318 | 1 | 50 | 2,656,656 | 33,634 | $0.776 |
| sympy__sympy-19495 | 0 | 39 | 2,119,352 | 38,337 | $0.938 |
| sympy__sympy-21379 | 1 | 24 | 1,152,459 | 25,642 | $0.384 |
| sympy__sympy-23413 | 0 | 31 | 1,614,387 | 33,585 | $1.073 |
