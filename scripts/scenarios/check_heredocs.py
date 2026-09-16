#!/usr/bin/env python3
"""Compile every heredoc the scenario arms feed to python3, whatever tag it uses.

`bash -n` treats a heredoc as opaque text, so a syntax error inside one is invisible until the
interpreter runs it — which, for these arms, is after an hour of gateway time has been spent.

TWO THINGS THIS DOES THAT A `<<'PY'` GREP DOES NOT, both of them ways the check could quietly stop
covering something:

  1. IT KEYS ON THE INVOCATION, NOT ON THE TAG. An earlier version matched the literal tag `PY`. Every
     arm happens to use it today, so the check passed and looked complete — but an arm added later
     with `<<'EOF' | python3` would have been skipped in silence, which is the failure mode the whole
     preflight exists to remove rather than relocate.
  2. IT ASSERTS ITS OWN COVERAGE. If a python3 heredoc is found whose body cannot be extracted, that
     is a failure, not a skip. A checker that silently finds nothing is indistinguishable from a clean
     tree, and this repo has paid for that shape more than once.

It lives in its own file rather than inside a heredoc in preflight.sh on purpose: a scanner for
heredocs, written inside a heredoc, matched its own pattern as if it were one.
"""

import glob
import os
import py_compile
import re
import sys
import tempfile

# A heredoc redirection on a line that also invokes python3. The tag may be quoted ('TAG', "TAG") or
# bare; `<<-` strips leading tabs from the terminator.
REDIR = re.compile(r"<<(-?)\s*(['\"]?)([A-Za-z_][A-Za-z0-9_]*)\2")


def blocks(path):
    """Yield (tag, quoted, body, start_line) for each python3 heredoc in path."""
    lines = open(path, encoding="utf-8").read().splitlines()
    i = 0
    while i < len(lines):
        line = lines[i]
        m = REDIR.search(line)
        if m and "python3" in line:
            dash, quote, tag = m.group(1), m.group(2), m.group(3)
            body, j = [], i + 1
            while j < len(lines):
                candidate = lines[j].lstrip("\t") if dash else lines[j]
                if candidate == tag:
                    break
                body.append(lines[j])
                j += 1
            else:
                # Ran off the end without finding the terminator: report rather than skip.
                yield tag, quote, None, i + 1
                return
            yield tag, quote, "\n".join(body), i + 1
            i = j
        i += 1


def main(root):
    paths = sorted(glob.glob(os.path.join(root, "*.sh")))
    if not paths:
        print("no .sh files under %s — the checker found nothing to check, which is a failure" % root)
        return 1

    found = bad = 0
    for path in paths:
        name = os.path.basename(path)
        for tag, quote, body, line in blocks(path):
            found += 1
            if body is None:
                print("UNTERMINATED: %s:%d heredoc <<%s never closed" % (name, line, tag))
                bad += 1
                continue
            if not quote:
                # The shell expands $vars in an unquoted heredoc, so what python receives is not
                # this text. Still worth compiling, but say so — it is a latent footgun.
                print("WARNING: %s:%d feeds python3 an UNQUOTED heredoc (<<%s); the shell expands it "
                      "first, so this check sees different text than python will" % (name, line, tag))
            tmp = None
            try:
                with tempfile.NamedTemporaryFile("w", suffix=".py", delete=False,
                                                 encoding="utf-8") as f:
                    f.write(body)
                    tmp = f.name
                py_compile.compile(tmp, doraise=True, cfile=tmp + "c")
            except py_compile.PyCompileError as e:
                print("SYNTAX (python): %s:%d\n  %s" % (name, line, e))
                bad += 1
            finally:
                for p in (tmp, tmp and tmp + "c"):
                    if p and os.path.exists(p):
                        os.unlink(p)

    # Coverage assertion. These arms are built around reading their own results in python; a tree with
    # none means either the arms changed shape or the matcher stopped matching, and both need a human.
    if found == 0:
        print("NO python3 heredocs found under %s. Every arm reads its results in python, so this "
              "means the matcher has stopped matching — not that there is nothing to check." % root)
        return 1
    print("compiled %d python3 heredoc(s) across %d file(s)" % (found, len(paths)))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1] if len(sys.argv) > 1 else os.path.dirname(os.path.abspath(__file__))))
