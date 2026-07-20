# Sample run — before / after full context

A single input context ([`context.before.json`](context.before.json)) run through each of the three configs, showing the complete `messages` array in and out. Captured with the store disabled and `marker_mode: off`, so outputs carry no `<<cg:…>>` markers and are drop-in inference-request bodies.

> The [`configs/`](configs) here are the committed [`../configs`](../configs) with triggers lowered so this small readable example actually fires. The LLM configs used `claude-haiku-4-5` via `model.source: config` (CHEAP_MODEL_* env).

| Config | messages | body bytes | output file |
|---|---|---|---|
| Config 1 | 11 → 11 | 2667 → 2376 | [`context.after.toon.json`](context.after.toon.json) |
| Config 2 | 11 → 11 | 2667 → 2145 | [`context.after.extract.json`](context.after.extract.json) |
| Config 3 | 11 → 4 | 2667 → 1298 | [`context.after.summarize.json`](context.after.summarize.json) |

## BEFORE — shared input (11 messages, 2667 bytes)

```
[0] system:
    You are a coding assistant.
[1] user:
    List the admins, then fix the failing test_col_insert.
[2] assistant:
    Listing users.
[3] tool (id=c1):
    [{"id": 1, "name": "Alice", "role": "admin", "active": true}, {"id": 2, "name": "Bob", "role": "member", "active": false}, {"id": 3, "name": "Carol", "role": "admin", "active": true}, {"id": 4, "name": "Dave", "role": "member", "active": true}, {"id": 5, "name": "Eve", "role": "admin", "active": false}, {"id": 6, "name": "Frank", "role": "member", "active": true}]
[4] assistant:
    Searching for col_insert references.
[5] tool (id=c2):
    sympy/matrices/common.py:80:    # ref to col_insert usage #0
    sympy/matrices/common.py:81:    # ref to col_insert usage #1
    sympy/matrices/common.py:82:    # ref to col_insert usage #2
    sympy/matrices/common.py:83:    # ref to col_insert usage #3
    sympy/matrices/common.py:84:    # ref to col_insert usage #4
    sympy/matrices/common.py:85:    # ref to col_insert usage #5
    sympy/matrices/common.py:86:    # ref to col_insert usage #6
    sympy/matrices/common.py:87:    # ref to col_insert usage #7
    sympy/matrices/common.py:88:    # ref to col_insert usage #8
    sympy/matrices/common.py:89:    # ref to col_insert usage #9
    sympy/matrices/common.py:90:    # ref to col_insert usage #10
    sympy/matrices/common.py:91:    # ref to col_insert usage #11
    sympy/matrices/common.py:92:    # ref to col_insert usage #12
    sympy/matrices/common.py:93:    # ref to col_insert usage #13
[6] assistant:
    Reading the implementation.
[7] tool (id=c3):
    def col_insert(self, pos, other):
        if pos < 0:
            pos = self.cols + pos
        # BUG: no upper clamp -> pos may exceed self.cols
        return self._eval_col_insert(pos, other)
        # helper line 0
        # helper line 1
        # helper line 2
        # helper line 3
        # helper line 4
        # helper line 5
        # helper line 6
        # helper line 7
        # helper line 8
        # helper line 9
        # helper line 10
        # helper line 11
[8] assistant:
    Running the failing test.
[9] tool (id=c4):
    $ pytest tests/test_matrices.py::test_col_insert
    tests/test_matrices.py::test_col_insert FAILED
    E   IndexError: Index out of range: a[2]
    sympy/matrices/common.py:86: IndexError
    1 failed, 180 passed in 2.14s
[10] user:
    Which users are admins, and what's the fix?
```

## AFTER — Config 1 — toon (deterministic reformatter, no LLM)

`11 → 11 messages, 2667 → 2376 bytes`

```
[0] system:
    You are a coding assistant.
[1] user:
    List the admins, then fix the failing test_col_insert.
[2] assistant:
    Listing users.
[3] tool (id=c1):
    [6]{active,id,name,role}:
    true,1,Alice,admin
    false,2,Bob,member
    true,3,Carol,admin
    true,4,Dave,member
    false,5,Eve,admin
    true,6,Frank,member

[4] assistant:
    Searching for col_insert references.
[5] tool (id=c2):
    sympy/matrices/common.py:80:    # ref to col_insert usage #0
    sympy/matrices/common.py:81:    # ref to col_insert usage #1
    sympy/matrices/common.py:82:    # ref to col_insert usage #2
    sympy/matrices/common.py:83:    # ref to col_insert usage #3
    sympy/matrices/common.py:84:    # ref to col_insert usage #4
    sympy/matrices/common.py:85:    # ref to col_insert usage #5
    sympy/matrices/common.py:86:    # ref to col_insert usage #6
    sympy/matrices/common.py:87:    # ref to col_insert usage #7
    sympy/matrices/common.py:88:    # ref to col_insert usage #8
    sympy/matrices/common.py:89:    # ref to col_insert usage #9
    sympy/matrices/common.py:90:    # ref to col_insert usage #10
    sympy/matrices/common.py:91:    # ref to col_insert usage #11
    sympy/matrices/common.py:92:    # ref to col_insert usage #12
    sympy/matrices/common.py:93:    # ref to col_insert usage #13
[6] assistant:
    Reading the implementation.
[7] tool (id=c3):
    def col_insert(self, pos, other):
        if pos < 0:
            pos = self.cols + pos
        # BUG: no upper clamp -> pos may exceed self.cols
        return self._eval_col_insert(pos, other)
        # helper line 0
        # helper line 1
        # helper line 2
        # helper line 3
        # helper line 4
        # helper line 5
        # helper line 6
        # helper line 7
        # helper line 8
        # helper line 9
        # helper line 10
        # helper line 11
[8] assistant:
    Running the failing test.
[9] tool (id=c4):
    $ pytest tests/test_matrices.py::test_col_insert
    tests/test_matrices.py::test_col_insert FAILED
    E   IndexError: Index out of range: a[2]
    sympy/matrices/common.py:86: IndexError
    1 failed, 180 passed in 2.14s
[10] user:
    Which users are admins, and what's the fix?
```

## AFTER — Config 2 — extract strategy=code (LLM: claude-haiku-4-5)

`11 → 11 messages, 2667 → 2145 bytes`

```
[0] system:
    You are a coding assistant.
[1] user:
    List the admins, then fix the failing test_col_insert.
[2] assistant:
    Listing users.
[3] tool (id=c1):
    [{"active":true,"id":1,"name":"Alice","role":"admin"},{"active":true,"id":3,"name":"Carol","role":"admin"},{"active":false,"id":5,"name":"Eve","role":"admin"}]

[4] assistant:
    Searching for col_insert references.
[5] tool (id=c2):
    sympy/matrices/common.py:80:    # ref to col_insert usage #0
    sympy/matrices/common.py:81:    # ref to col_insert usage #1
    sympy/matrices/common.py:82:    # ref to col_insert usage #2
    sympy/matrices/common.py:83:    # ref to col_insert usage #3
    sympy/matrices/common.py:84:    # ref to col_insert usage #4
    sympy/matrices/common.py:85:    # ref to col_insert usage #5
    sympy/matrices/common.py:86:    # ref to col_insert usage #6
    sympy/matrices/common.py:87:    # ref to col_insert usage #7
    sympy/matrices/common.py:88:    # ref to col_insert usage #8
    sympy/matrices/common.py:89:    # ref to col_insert usage #9
    sympy/matrices/common.py:90:    # ref to col_insert usage #10
    sympy/matrices/common.py:91:    # ref to col_insert usage #11
    sympy/matrices/common.py:92:    # ref to col_insert usage #12
    sympy/matrices/common.py:93:    # ref to col_insert usage #13
[6] assistant:
    Reading the implementation.
[7] tool (id=c3):
    def col_insert(self, pos, other):
        if pos < 0:
            pos = self.cols + pos
        # BUG: no upper clamp -> pos may exceed self.cols
        return self._eval_col_insert(pos, other)

[8] assistant:
    Running the failing test.
[9] tool (id=c4):
    $ pytest tests/test_matrices.py::test_col_insert
    tests/test_matrices.py::test_col_insert FAILED
    E   IndexError: Index out of range: a[2]
    sympy/matrices/common.py:86: IndexError

[10] user:
    Which users are admins, and what's the fix?
```

## AFTER — Config 3 — summarize (LLM: claude-haiku-4-5)

`11 → 4 messages, 2667 → 1298 bytes`

```
[0] system:
    You are a coding assistant.
[1] system:
    === History Summary ===
    The earlier trajectory is summarized below.

    <summary>
    • Essential Information:
      - The user requested two tasks: (1) list the admins, and (2) fix the failing test_col_insert
      - The assistant took the following actions in sequence:
        1. Listed users
        2. Searched for col_insert references
        3. Read the implementation
        4. Ran the failing test
      - All tool outputs have been masked/removed from the trajectory, making the specific results of these actions unavailable
      - No explicit information about which users are admins or what the test failure is or its fix has been provided in the visible conversation
    </summary>

    Use this summary as the older context, and use the following messages as the most recent context. Continue the task accordingly. Do not summarize the conversation again.
[2] tool (id=c4):
    $ pytest tests/test_matrices.py::test_col_insert
    tests/test_matrices.py::test_col_insert FAILED
    E   IndexError: Index out of range: a[2]
    sympy/matrices/common.py:86: IndexError
    1 failed, 180 passed in 2.14s
[3] user:
    Which users are admins, and what's the fix?
```
