# Before → After showcase

One input context run through each of the three [llm-d compaction service](llm-d-service.md)
configs, showing what each did to the same `messages` array. Captured with the store disabled and
`marker_mode: off`, so outputs carry no `<<cg:…>>` markers and are drop-in inference-request bodies.

!!! note "About this capture"
    The configs here are the committed ones with triggers lowered so this small readable example
    actually fires. The two LLM configs used **`claude-haiku-4-5`** via `model.source: config`
    (`CHEAP_MODEL_*` env).

## The numbers

| Config | messages | body bytes |
|---|---|---|
| **before** (shared input) | 11 | 2667 |
| **TOON** (`format` + `toon`, no LLM) | 11 | 2376 |
| **Extract** (`strategy: code`, LLM) | 11 | 2145 |
| **Summarize** (LLM) | 4 | 1298 |

TOON and Extract keep all 11 messages and shrink content in place. Summarize is the outlier — it
collapses 11 messages to 4 by restructuring the transcript.

## The shared input

The task: *"List the admins, then fix the failing test_col_insert."* The 11-message transcript
carries a 6-user JSON array (tool `c1`), 14 lines of `col_insert` grep hits (`c2`), the buggy
implementation (`c3`), and a failing pytest run (`c4`), then a final user question:
*"Which users are admins, and what's the fix?"*

## What each config did

=== "TOON"

    `11 → 11 messages, 2667 → 2376 bytes` — deterministic, no LLM.

    `toon` rewrote the user-list JSON array in tool `c1` to **TOON**: the field names are declared
    once as a header, then one compact row per element. Everything else is untouched.

    ```text
    before (tool c1):
        [{"id": 1, "name": "Alice", "role": "admin", "active": true}, {"id": 2, ...}, ...]

    after (tool c1):
        [6]{active,id,name,role}:
        true,1,Alice,admin
        false,2,Bob,member
        true,3,Carol,admin
        true,4,Dave,member
        false,5,Eve,admin
        true,6,Frank,member
    ```

    Lossless repack: same values, fewer tokens. The grep hits, implementation, and pytest run
    pass through unchanged.

=== "Extract"

    `11 → 11 messages, 2667 → 2145 bytes` — LLM (`claude-haiku-4-5`), `strategy: code`.

    `extract` had a cheap LLM write a sandboxed, deletion-only filter tuned to *this* content and
    the current query. It made two content-specific cuts:

    - **tool `c1`** — filtered the 6-user array down to just the **three admins** (Alice, Carol,
      Eve), verbatim, dropping the non-admin members.
    - **tool `c3`** — trimmed the implementation to the buggy lines, dropping the 12 lines of
      `# helper line N` noise below the bug.
    - **tool `c4`** — dropped the trailing `1 failed, 180 passed` summary line, keeping the
      failure and the `IndexError` traceback.

    ```text
    after (tool c1):
        [{"active":true,"id":1,"name":"Alice","role":"admin"},
         {"active":true,"id":3,"name":"Carol","role":"admin"},
         {"active":false,"id":5,"name":"Eve","role":"admin"}]
    ```

    Deletion-only: the model can trim but provably cannot fabricate or reword.

=== "Summarize"

    `11 → 4 messages, 2667 → 1298 bytes` — LLM (`claude-haiku-4-5`).

    `summarize` collapsed the middle of the transcript into a single
    **`=== History Summary ===`** system message, keeping the head and the last couple of turns.
    The result is `[system, <History Summary>, tool c4, final user question]`.

    ```text
    [1] system:
        === History Summary ===
        The earlier trajectory is summarized below.

        <summary>
        • The user requested two tasks: (1) list the admins, (2) fix the failing test_col_insert
        • The assistant listed users, searched col_insert refs, read the impl, ran the failing test
        • All tool outputs were masked, so specific results are unavailable
        </summary>

        Use this summary as the older context ... Continue the task accordingly.
    ```

    The summarizer is grounded in the current task (the first user turn + recent turns are passed
    as "summarize toward this"), not a blind digest. It changes the message count — the one
    component that does — so it runs alone.

See how these configs are wired in the [llm-d compaction service](llm-d-service.md) guide.
