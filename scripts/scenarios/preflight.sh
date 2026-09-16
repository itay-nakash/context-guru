#!/bin/bash
# PREFLIGHT — the checks that must not be discovered by a paid run.
#
# Every arm here costs real money and roughly an hour of wall-clock, most of it sleeping past cache
# TTLs. A syntax error in an arm's embedded python is invisible to `bash -n` (a heredoc is just text
# to the shell), so without this the thing that finds one is a run that has already spent its budget
# and then dies at the summary step with nothing to show.
#
# This exists because a commit message once claimed the compile check was in the repo when it was
# only ever run by hand during development. A check that lives in someone's shell is not a check.
#
# Run by run-all.sh before the first arm, and safe to run alone: it starts nothing, spends nothing,
# and needs no CG_SCEN_* configuration.
set -u
here="$(cd "$(dirname "$0")" && pwd)"
fail=0

# 1. Shell syntax, every arm and the library.
for f in "$here"/*.sh; do
  bash -n "$f" || { echo "SYNTAX (bash): $f"; fail=1; }
done

# 2. Python syntax, for every heredoc the arms feed to python3. `bash -n` cannot see inside one.
python3 - "$here" <<'PY' || fail=1
import glob, os, re, sys, tempfile, py_compile

here = sys.argv[1]
bad = 0
for path in sorted(glob.glob(os.path.join(here, '*.sh'))):
    src = open(path, encoding='utf-8').read()
    # Match the heredoc forms the arms use, quoted so the shell does not expand them.
    blocks = re.findall(r"<<'PY(?:EOF)?'\n(.*?)\nPY(?:EOF)?\n", src, re.S)
    for i, b in enumerate(blocks):
        with tempfile.NamedTemporaryFile('w', suffix='.py', delete=False, encoding='utf-8') as f:
            f.write(b)
            tmp = f.name
        try:
            py_compile.compile(tmp, doraise=True, cfile=tmp + 'c')
        except py_compile.PyCompileError as e:
            print('SYNTAX (python): %s block %d\n  %s' % (os.path.basename(path), i, e))
            bad += 1
        finally:
            os.unlink(tmp)
            if os.path.exists(tmp + 'c'):
                os.unlink(tmp + 'c')
sys.exit(1 if bad else 0)
PY

# 3. sqlite3 and curl are what every arm reads its results with; python3 is what interprets them.
for cmd in python3 sqlite3 curl; do
  command -v "$cmd" >/dev/null || { echo "MISSING: $cmd"; fail=1; }
done

if [ "$fail" -ne 0 ]; then
  echo "PREFLIGHT FAILED — fix the above before spending an hour of gateway time."
  exit 1
fi
echo "preflight ok"
