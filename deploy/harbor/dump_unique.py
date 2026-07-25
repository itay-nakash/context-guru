#!/usr/bin/env python3
"""Per-component CUMULATIVE-vs-UNIQUE savings from a CONTEXT_GURU_DUMP change log.

The agent re-sends its history verbatim every turn, and the proxy re-compacts it, so
/stats sums the same compaction many times (cumulative). This dedups each distinct
compaction by the hash of its BEFORE content to report the honest UNIQUE tokens saved
and the over-count ratio. Category is inferred from the marker text (the dump has no
component field).

Usage: dump_unique.py <dump.jsonl> [--json out.json]
"""
import argparse, json, hashlib
from collections import defaultdict


def category(after):
    if "superseded by a later failed" in after:
        return "failed_run"
    if "identical to an earlier" in after:
        return "dedup"
    if "older tool output masked" in after:
        return "mask"
    if "evicted to fit context" in after:
        return "phi_evict"
    if "lines omitted)" in after:
        return "collapse"
    if "<<cg:" in after:
        return "extract/extract_llm"
    return "cmdfilter(inplace)"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("dump")
    ap.add_argument("--json", default="")
    a = ap.parse_args()
    cum = defaultdict(int)
    uniq = defaultdict(int)
    seen = defaultdict(set)
    cnt = defaultdict(int)
    recs = 0
    for line in open(a.dump):
        line = line.strip()
        if not line:
            continue
        r = json.loads(line)
        recs += 1
        for ch in r.get("changes", []):
            after = ch.get("after", "")
            before = ch.get("before", "")
            saved = ch.get("before_tokens", 0) - ch.get("after_tokens", 0)
            if saved <= 0:
                continue
            c = category(after)
            cnt[c] += 1
            cum[c] += saved
            h = hashlib.sha1(before.encode()).hexdigest()
            if h not in seen[c]:
                seen[c].add(h)
                uniq[c] += saved
    out = {"dump_records": recs, "components": {}}
    print(f"dump records: {recs}")
    print(f"{'category':22s} {'changes':>8} {'distinct':>9} {'cum_saved':>11} {'uniq_saved':>11} {'overcount':>10}")
    for c in sorted(cum, key=lambda k: -uniq[k]):
        ratio = cum[c] / uniq[c] if uniq[c] else 0
        print(f"{c:22s} {cnt[c]:8d} {len(seen[c]):9d} {cum[c]:11,} {uniq[c]:11,} {ratio:9.1f}x")
        out["components"][c] = {"changes": cnt[c], "distinct": len(seen[c]),
                                "cum_saved": cum[c], "uniq_saved": uniq[c], "overcount_ratio": round(ratio, 2)}
    tot_cum = sum(cum.values()); tot_uniq = sum(uniq.values())
    print(f"{'TOTAL':22s} {'':>8} {'':>9} {tot_cum:11,} {tot_uniq:11,}")
    out["total_cum_saved"] = tot_cum
    out["total_uniq_saved"] = tot_uniq
    if a.json:
        json.dump(out, open(a.json, "w"), indent=1)
        print(f"wrote {a.json}")


if __name__ == "__main__":
    main()
