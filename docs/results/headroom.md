# Full results — headroom (hd-cache) (SWE-bench Verified, 50 tasks)

Live through the harness, `claude-code` agent on `aws/claude-sonnet-5`. Cache-aware billed input cost (fresh $2/M · cache-read $0.20/M · cache-write $2.50/M) + output $10/M, recomputed from each trial's token tiers. See [REPRODUCE.md](REPRODUCE.md).

## Totals

| tasks scored | solved | rate | total billed cost | mean steps | cache-hit | agent wall (sum) |
|---|---|---|---|---|---|---|
| 50 | 40 | 80% | $30.30 | 35.1 | 98.0% | 304 min |

## Per-task

| task | reward | steps | cache_read | cache_write | billed cost |
|---|---|---|---|---|---|
| astropy__astropy-12907 | 1 | 18 | 748,919 | 57,946 | $0.337 |
| astropy__astropy-14365 | 1 | 26 | 1,272,402 | 58,903 | $0.476 |
| astropy__astropy-8707 | 0 | 69 | 4,138,129 | 51,704 | $1.107 |
| django__django-11095 | 1 | 31 | 1,345,510 | 25,951 | $0.407 |
| django__django-11211 | 1 | 88 | 5,431,961 | 60,005 | $1.474 |
| django__django-11477 | 1 | 67 | 4,107,126 | 51,990 | $1.150 |
| django__django-11790 | 1 | 21 | 861,970 | 20,021 | $0.266 |
| django__django-12050 | 1 | 15 | 779,835 | 36,263 | $0.268 |
| django__django-12308 | 1 | 25 | 1,215,734 | 32,452 | $0.366 |
| django__django-12858 | 0 | 46 | 2,429,734 | 40,825 | $0.827 |
| django__django-13128 | 1 | 44 | 2,494,094 | 119,734 | $1.042 |
| django__django-13363 | 1 | 37 | 2,141,441 | 43,113 | $0.637 |
| django__django-13568 | 1 | 21 | 874,455 | 22,660 | $0.270 |
| django__django-13810 | 1 | 25 | 1,331,718 | 40,535 | $0.456 |
| django__django-14034 | 0 | 17 | 728,678 | 21,269 | $0.276 |
| django__django-14349 | 1 | 9 | 326,266 | 18,376 | $0.128 |
| django__django-14559 | 1 | 25 | 1,082,511 | 28,069 | $0.331 |
| django__django-14792 | 0 | 60 | 3,626,095 | 41,427 | $1.067 |
| django__django-15128 | 1 | 43 | 2,294,587 | 39,154 | $0.657 |
| django__django-15380 | 1 | 26 | 1,208,799 | 28,913 | $0.369 |
| django__django-15572 | 1 | 14 | 558,435 | 19,735 | $0.189 |
| django__django-15930 | 1 | 28 | 1,295,892 | 26,081 | $0.399 |
| django__django-16145 | 1 | 24 | 1,028,867 | 22,790 | $0.312 |
| django__django-16502 | 0 | 38 | 1,916,315 | 30,526 | $0.604 |
| django__django-16667 | 0 | 15 | 602,641 | 19,496 | $0.191 |
| django__django-17087 | 1 | 20 | 828,058 | 21,272 | $0.253 |
| matplotlib__matplotlib-22719 | 1 | 28 | 1,338,523 | 28,324 | $0.407 |
| matplotlib__matplotlib-24570 | 1 | 22 | 961,212 | 21,954 | $0.364 |
| matplotlib__matplotlib-25775 | 1 | 135 | 13,265,857 | 104,983 | $3.485 |
| psf__requests-1142 | 1 | 14 | 541,350 | 19,393 | $0.190 |
| pydata__xarray-3151 | 1 | 19 | 824,871 | 24,550 | $0.270 |
| pydata__xarray-4966 | 1 | 25 | 1,195,388 | 31,643 | $0.380 |
| pylint-dev__pylint-4551 | 1 | 45 | 2,801,827 | 52,055 | $0.861 |
| pytest-dev__pytest-10051 | 1 | 17 | 682,749 | 17,574 | $0.213 |
| pytest-dev__pytest-7205 | 1 | 48 | 2,502,813 | 39,914 | $0.752 |
| scikit-learn__scikit-learn-10844 | 1 | 10 | 363,542 | 15,746 | $0.130 |
| scikit-learn__scikit-learn-13328 | 1 | 21 | 866,537 | 20,972 | $0.267 |
| scikit-learn__scikit-learn-14894 | 1 | 20 | 828,462 | 20,742 | $0.271 |
| scikit-learn__scikit-learn-9288 | 1 | 30 | 1,476,615 | 31,687 | $0.476 |
| sphinx-doc__sphinx-7454 | 1 | 26 | 1,287,627 | 32,187 | $0.406 |
| sphinx-doc__sphinx-8120 | 1 | 74 | 4,375,724 | 52,043 | $1.231 |
| sphinx-doc__sphinx-8638 | 0 | 32 | 1,068,489 | 56,612 | $0.543 |
| sphinx-doc__sphinx-9602 | 0 | 56 | 3,376,186 | 45,137 | $1.036 |
| sympy__sympy-13031 | 1 | 42 | 1,924,725 | 30,000 | $0.727 |
| sympy__sympy-13877 | 1 | 16 | 649,900 | 49,201 | $0.302 |
| sympy__sympy-15599 | 1 | 45 | 2,261,362 | 36,297 | $0.718 |
| sympy__sympy-17318 | 1 | 75 | 4,015,695 | 38,471 | $1.127 |
| sympy__sympy-19495 | 1 | 45 | 2,311,024 | 33,835 | $0.818 |
| sympy__sympy-21379 | 0 | 26 | 1,164,192 | 23,205 | $0.422 |
| sympy__sympy-23413 | 0 | 32 | 1,645,502 | 33,325 | $1.046 |
