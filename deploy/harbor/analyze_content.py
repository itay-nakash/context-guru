#!/usr/bin/env python3
"""Where are the tokens? Categorize tool-output token mass in real captured requests
so component investment is evidence-driven, not guessed. Also measures exact-duplicate
mass and same-resource re-read mass (the ceilings for dedup / a supersede-reads lever).

Usage: analyze_content.py <capture.jsonl> [more.jsonl ...]
"""
import json, sys, re, hashlib
from collections import defaultdict

TOK = lambda s: max(1, len(s) // 4)  # cheap ~4 chars/token proxy (consistent across categories)
LINE_NO = re.compile(r"^\s{0,6}\d+[\t ]")
TEST = re.compile(r"(\d+ (passed|failed|error)|=+ (FAILURES|test session)|Traceback \(most recent|PASSED|FAILED)")
SEARCH = re.compile(r"^\S+:\d+:", re.M)
DIFF = re.compile(r"^(diff --git|@@ |commit [0-9a-f]{7})", re.M)
INSTALL = re.compile(r"(Requirement already satisfied|Collecting |Installing collected|pip install)")


def looks_file_read(s):
    lines = [l for l in s.split("\n") if l.strip()]
    if len(lines) < 8:
        return False
    numbered = sum(1 for l in lines[:40] if LINE_NO.match(l))
    return numbered * 100 // min(len(lines), 40) >= 60


def categorize(s):
    if looks_file_read(s):
        return "file_read"
    if INSTALL.search(s):
        return "install_log"
    if DIFF.search(s):
        return "diff"
    if TEST.search(s):
        return "test_log"
    if len(SEARCH.findall(s)) >= 3:
        return "search"
    return "other"


def tool_texts(body, provider):
    """Yield (text, tool_name_hint) for each tool output in the request."""
    out = []
    if provider == "anthropic":
        for m in body.get("messages", []):
            c = m.get("content")
            if m.get("role") == "user" and isinstance(c, list):
                for b in c:
                    if isinstance(b, dict) and b.get("type") == "tool_result":
                        t = b.get("content")
                        if isinstance(t, str):
                            out.append(t)
                        elif isinstance(t, list):
                            out.append("".join(x.get("text", "") for x in t if isinstance(x, dict)))
    else:  # openai
        for m in body.get("messages", []):
            if m.get("role") == "tool":
                c = m.get("content")
                out.append(c if isinstance(c, str) else json.dumps(c))
    return out


def main():
    files = sys.argv[1:]
    # group requests by conversation (first user msg), take the LARGEST (latest) request per conv
    for f in files:
        recs = [json.loads(l) for l in open(f) if l.strip()]
        by_conv = defaultdict(list)
        for r in recs:
            b = r["body"]
            key = None
            for m in b.get("messages", []):
                if m.get("role") == "user":
                    key = hashlib.sha1(json.dumps(m.get("content"))[:200].encode()).hexdigest()[:8]
                    break
            by_conv[key].append(r)
        cat_tok = defaultdict(int)
        total = 0
        exact_dup_tok = 0
        reread_tok = 0  # same file_read path seen more than once (only extra copies)
        largest = None
        for conv, rs in by_conv.items():
            rs.sort(key=lambda r: len(r["body"].get("messages", [])))
            r = rs[-1]  # largest/latest request in the conversation
            prov = r.get("provider", "anthropic")
            texts = tool_texts(r["body"], prov)
            seen_hash = {}
            seen_path = {}
            for t in texts:
                if not t:
                    continue
                tk = TOK(t)
                total += tk
                cat_tok[categorize(t)] += tk
                h = hashlib.sha1(t.encode()).hexdigest()
                if h in seen_hash:
                    exact_dup_tok += tk
                seen_hash[h] = 1
                if looks_file_read(t):
                    # crude "resource id" = first ~120 chars (path banners differ per tool)
                    rid = t[:120]
                    if rid in seen_path:
                        reread_tok += tk
                    seen_path[rid] = 1
            if largest is None or tk > 0 and len(r["body"].get("messages", [])) > largest[0]:
                largest = (len(r["body"].get("messages", [])), conv, texts)
        print(f"\n===== {f} =====")
        print(f"tool-output tokens (largest req per conv, summed): {total:,}")
        for c, v in sorted(cat_tok.items(), key=lambda x: -x[1]):
            print(f"  {c:12s} {v:9,}  ({100*v//max(total,1):2d}%)")
        print(f"  exact-duplicate tool-output tokens (dedup ceiling): {exact_dup_tok:,} ({100*exact_dup_tok//max(total,1)}%)")
        print(f"  same-file re-read extra tokens (supersede-reads ceiling): {reread_tok:,} ({100*reread_tok//max(total,1)}%)")


if __name__ == "__main__":
    main()
