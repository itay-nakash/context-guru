#!/usr/bin/env python3
"""Classify EVERY heredoc in the scenario arms, and compile the ones that feed python3.

`bash -n` treats a heredoc as opaque text, so a syntax error inside one is invisible until the
interpreter runs it — which for these arms is after an hour of paid gateway time.

# THE ASSERTION IS OVER THE POPULATION, NOT OVER WHAT THE MATCHER FOUND

This is the third rewrite of this check and the first one built on the right principle. The two before
it both looked complete and both had a silent hole, for the same reason each time — they counted the
heredocs their own pattern liked, and reported success on the count:

  1. Matched the literal tag `PY`. An arm using `<<'EOF' | python3` was skipped in silence.
  2. Keyed on the python3 invocation instead, which fixed that instance and left the shape intact:
     a hyphenated tag (`<<'PY-BODY'`, legal bash) fell out of the tag character class, and a line
     continuation between `python3` and the `<<` put them on different physical lines. Both dropped
     6 blocks to 5 and printed it as a success.

`found == 0` cannot see 6 → 5. That is the shape that ships, because everything still says ok.

So this enumerates every heredoc redirection in these files and requires each one to be CLASSIFIED:

  - fed to python3  -> compile it, and a syntax error fails
  - fed to `cat`    -> a data file (lib.sh writes config.yaml this way); allowed, not compiled
  - anything else    -> FAILURE, named as unclassified

An unmatched tag, a continuation, a new interpreter, a heredoc nobody thought about: all surface as
"I could not classify this" rather than as absence. Absence is what the previous two versions
reported.
"""

import glob
import os
import py_compile
import re
import sys
import tempfile

# A heredoc redirection. The tag may be quoted or bare; `<<-` strips leading tabs from the terminator.
# The tag class is deliberately permissive — hyphens and dots are legal, and being narrow here is
# exactly how version 2 lost a block. `<<<` (herestring) is excluded: it carries no body.
REDIR = re.compile(r"<<(-?)\s*(?!<)(?:'([^']+)'|\"([^\"]+)\"|([A-Za-z_][\w.-]*))")


def logical_lines(lines):
    """Join backslash continuations, yielding (text, first_physical_index, last_physical_index).

    Version 2 required `python3` and the `<<` on the same PHYSICAL line, so splitting them across a
    continuation hid the block. These invocation lines are already long enough that wrapping one is
    the natural next edit.
    """
    i = 0
    while i < len(lines):
        start = i
        text = lines[i]
        while text.endswith("\\") and i + 1 < len(lines):
            i += 1
            text = text[:-1] + " " + lines[i]
        yield text, start, i
        i += 1


def is_comment(text):
    return text.lstrip().startswith("#")


def classify(text):
    """What does this command feed the heredoc to? None means 'cannot tell'."""
    if re.search(r"\bpython3?\b", text):
        return "python"
    if re.search(r"\bcat\b", text):
        return "data"
    return None


def heredocs(path):
    """Yield (kind, tag, body, physical_line, text) for every heredoc in path.

    body is None when the terminator is never found.
    """
    lines = open(path, encoding="utf-8").read().splitlines()
    consumed_to = -1
    for text, first, last in logical_lines(lines):
        if last <= consumed_to or is_comment(text):
            continue
        for m in REDIR.finditer(text):
            dash = m.group(1)
            tag = m.group(2) or m.group(3) or m.group(4)
            body, j = [], last + 1
            found_end = False
            while j < len(lines):
                candidate = lines[j].lstrip("\t") if dash else lines[j]
                if candidate.strip() == tag:
                    found_end = True
                    break
                body.append(lines[j])
                j += 1
            consumed_to = j
            yield (classify(text), tag, "\n".join(body) if found_end else None, first + 1, text)


def compile_body(body):
    tmp = None
    try:
        with tempfile.NamedTemporaryFile("w", suffix=".py", delete=False, encoding="utf-8") as f:
            f.write(body)
            tmp = f.name
        py_compile.compile(tmp, doraise=True, cfile=tmp + "c")
        return None
    except py_compile.PyCompileError as e:
        return str(e)
    finally:
        for p in (tmp, tmp and tmp + "c"):
            if p and os.path.exists(p):
                os.unlink(p)


def main(root):
    paths = sorted(glob.glob(os.path.join(root, "*.sh")))
    if not paths:
        print("no .sh files under %s — nothing to check is itself a failure" % root)
        return 1

    compiled = data = bad = 0
    for path in paths:
        name = os.path.basename(path)
        for kind, tag, body, line, text in heredocs(path):
            if body is None:
                print("UNTERMINATED: %s:%d heredoc <<%s never closed" % (name, line, tag))
                bad += 1
                continue
            if kind is None:
                # THE POINT OF THE REWRITE. Not skipped, not counted as fine: named and failed.
                print("UNCLASSIFIED: %s:%d heredoc <<%s — cannot tell what consumes this body.\n"
                      "  %s\n"
                      "  If it is python, make the invocation recognisable; if it is data, pipe it "
                      "through cat; otherwise teach classify() about it." % (name, line, tag, text.strip()))
                bad += 1
                continue
            if kind == "data":
                data += 1
                continue
            if "'" not in text.split("<<")[1][:2] and '"' not in text.split("<<")[1][:2]:
                print("WARNING: %s:%d feeds python an UNQUOTED heredoc (<<%s); the shell expands it "
                      "first, so this check sees different text than python will" % (name, line, tag))
            err = compile_body(body)
            if err:
                print("SYNTAX (python): %s:%d\n  %s" % (name, line, err))
                bad += 1
            else:
                compiled += 1

    total = compiled + data + bad
    if total == 0:
        print("NO heredocs found under %s. Every arm reads its results through one, so this means the "
              "scanner has stopped matching — not that there is nothing to check." % root)
        return 1
    if compiled == 0:
        print("NO python heredocs found among %d heredoc(s). Every arm reads its results in python, so "
              "this means classification has broken, not that there is nothing to compile." % total)
        return 1
    print("heredocs: %d python compiled, %d data, %d problem(s)" % (compiled, data, bad))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else os.path.dirname(os.path.abspath(__file__))))
