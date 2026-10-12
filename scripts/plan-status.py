import json
import re
import sys


def main() -> int:
    if len(sys.argv) != 5:
        print("usage: plan-status.py <plan.yaml> <item-id> <status> <evidence.json>", file=sys.stderr)
        return 2
    path, item, status, evidence_path = sys.argv[1:]
    if status not in ("todo", "doing", "done", "blocked"):
        print(f"bad status {status}", file=sys.stderr)
        return 2
    with open(evidence_path) as fh:
        evidence = json.load(fh)
    if not isinstance(evidence, list) or not all(isinstance(e, str) for e in evidence):
        print("evidence must be a JSON list of strings", file=sys.stderr)
        return 2
    lines = open(path).read().split("\n")
    start = next((i for i, l in enumerate(lines) if l == f"  - id: {item}"), None)
    if start is None:
        print(f"no item {item} in {path}", file=sys.stderr)
        return 1
    end = next((i for i in range(start + 1, len(lines)) if re.match(r"^  - id: |^[a-z_]+:", lines[i])), len(lines))
    s = next((i for i in range(start, end) if lines[i].startswith("    status: ")), None)
    if s is None:
        print(f"item {item} has no status line", file=sys.stderr)
        return 1
    e = s + 1
    if not lines[e].startswith("    evidence:"):
        print(f"item {item}: evidence must follow status", file=sys.stderr)
        return 1
    stop = e + 1
    while stop < end and lines[stop].startswith("      "):
        stop += 1
    existing = []
    if lines[e].strip() != "evidence: []":
        existing = [l for l in lines[e + 1:stop]]
    new_items = [f"      - {json.dumps(x, ensure_ascii=True)}" for x in evidence]
    block = [f"    status: {status}", "    evidence:"] + existing + new_items
    lines[s:stop] = block
    open(path, "w").write("\n".join(lines))
    print(f"{item}: status={status}, +{len(evidence)} evidence")
    return 0


if __name__ == "__main__":
    sys.exit(main())
