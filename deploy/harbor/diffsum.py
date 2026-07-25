#!/usr/bin/env python3
"""Summarize what a component actually did to the messages: given before/after
request bodies (as written by replay2 under /tmp/cg-runs/diffs/<tag>/<comp>/), find
the messages whose text changed and print a compact before->after snippet for each.
Lets a reviewer judge correctness / over-aggression without wading through the full
100KB+ bodies. Also flags structural changes (msg/tool_result/tool_use counts).

Usage: diffsum.py <dir-or-before.json> [after.json] [--head 220] [--tail 160]
"""
import argparse, json, sys
from pathlib import Path


def msg_texts(body):
    """Return list of (role, kind, text) for each content-bearing block, so we can
    align before/after and show only what changed."""
    out = []
    for mi, m in enumerate(body.get("messages", [])):
        role = m.get("role")
        c = m.get("content")
        if isinstance(c, str):
            out.append((mi, role, "text", c))
        elif isinstance(c, list):
            for bi, b in enumerate(c):
                if not isinstance(b, dict):
                    continue
                t = b.get("type")
                if t == "text":
                    out.append((mi, role, "text", b.get("text", "")))
                elif t == "tool_result":
                    cc = b.get("content")
                    if isinstance(cc, str):
                        out.append((mi, role, "tool_result", cc))
                    elif isinstance(cc, list):
                        for x in cc:
                            if isinstance(x, dict) and x.get("type") == "text":
                                out.append((mi, role, "tool_result", x.get("text", "")))
    return out


def struct(body):
    msgs = body.get("messages", [])
    tr = tu = 0
    for m in msgs:
        c = m.get("content")
        if isinstance(c, list):
            tr += sum(1 for b in c if isinstance(b, dict) and b.get("type") == "tool_result")
            tu += sum(1 for b in c if isinstance(b, dict) and b.get("type") == "tool_use")
        if isinstance(m.get("tool_calls"), list):
            tu += len(m["tool_calls"])
    return len(msgs), tr, tu


def clip(s, head, tail):
    s = s.replace("\n", "\\n")
    if len(s) <= head + tail + 20:
        return s
    return s[:head] + f"  …[{len(s)-head-tail} chars]…  " + s[-tail:]


def summarize(before_p, after_p, head, tail):
    b = json.loads(Path(before_p).read_bytes())
    a = json.loads(Path(after_p).read_bytes())
    sb, sa = struct(b), struct(a)
    print(f"# {Path(before_p).parent.name}/{Path(before_p).stem}")
    print(f"struct (msgs,tool_results,tool_uses): {sb} -> {sa}  bytes {len(json.dumps(b))}->{len(json.dumps(a))}")
    if sb != sa:
        print(f"  ** STRUCTURE CHANGED ** {sb}->{sa}")
    tb, ta = msg_texts(b), msg_texts(a)
    # align by (msg_index, role, kind) position; report changed texts
    changed = 0
    n = min(len(tb), len(ta))
    for i in range(n):
        (mi, role, kind, xb) = tb[i]
        (_, _, _, xa) = ta[i]
        if xb != xa:
            changed += 1
            print(f"\n  [{role}/{kind} #{mi}]  {len(xb)}->{len(xa)} chars")
            print(f"    BEFORE: {clip(xb, head, tail)}")
            print(f"    AFTER : {clip(xa, head, tail)}")
    if len(tb) != len(ta):
        print(f"\n  (block count changed {len(tb)}->{len(ta)})")
    if changed == 0 and sb == sa:
        print("  (no text-block changes detected at aligned positions)")
    print()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("target", help="a before.json file, or a dir of reqN.before/after pairs")
    ap.add_argument("after", nargs="?", default=None)
    ap.add_argument("--head", type=int, default=220)
    ap.add_argument("--tail", type=int, default=160)
    a = ap.parse_args()
    p = Path(a.target)
    if p.is_dir():
        for bf in sorted(p.glob("*.before.json")):
            summarize(bf, str(bf).replace(".before.", ".after."), a.head, a.tail)
    else:
        summarize(a.target, a.after or str(a.target).replace(".before.", ".after."), a.head, a.tail)


if __name__ == "__main__":
    main()
