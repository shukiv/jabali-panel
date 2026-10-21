#!/usr/bin/env python3
"""sast-summary.py GOSEC.json SEMGREP.json

Prints a Markdown job summary of a gosec report (-fmt json) and a Semgrep
report (--json): each tool's totals by severity, then the findings on lines
a PR adds, when BASE (the PR's base commit) is set. Advisory: always exits 0.
"""
import collections
import json
import os
import re
import subprocess
import sys

HUNK = re.compile(r"^@@ -\d+(?:,\d+)? \+(\d+)(?:,(\d+))? @@")


def load(path):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return None


def added_lines(base):
    """{path: set of line numbers} the diff from base to HEAD adds."""
    out = subprocess.run(["git", "diff", "-U0", "--no-color", "--no-ext-diff", base, "HEAD"],
                         capture_output=True, text=True, check=True).stdout
    added, path = collections.defaultdict(set), None
    for line in out.splitlines():
        if line.startswith("+++ "):
            path = line[6:] if line.startswith("+++ b/") else None
        elif path and (m := HUNK.match(line)):
            start, count = int(m.group(1)), int(m.group(2) or 1)
            added[path].update(range(start, start + count))
    return added


def rel(path):
    return os.path.relpath(path) if os.path.isabs(path) else path


def gosec_findings(report):
    for i in report.get("Issues") or []:
        line = int(str(i.get("line", "0")).split("-")[0] or 0)
        yield rel(i.get("file", "")), line, i.get("rule_id", ""), i.get("severity", ""), i.get("details", "")


def semgrep_findings(report):
    for r in report.get("results") or []:
        extra = r.get("extra", {})
        yield (r.get("path", ""), r.get("start", {}).get("line", 0), r.get("check_id", "").split(".")[-1],
               extra.get("severity", ""), extra.get("message", ""))


def cell(text):
    return " ".join(str(text).split()).replace("|", "\\|")[:140]


def main(argv):
    if len(argv) != 3:
        print(__doc__.strip(), file=sys.stderr)
        return 0
    base = os.environ.get("BASE", "")
    added = added_lines(base) if base else None
    print("### Static analysis (advisory)\n")
    on_pr = []
    for tool, path, parse in (("gosec", argv[1], gosec_findings), ("Semgrep", argv[2], semgrep_findings)):
        report = load(path)
        if report is None:
            print(f"- **{tool}**: no report, the scan did not run.")
            continue
        found = list(parse(report))
        by_sev = collections.Counter(f[3] for f in found)
        counts = ", ".join(f"{n} {s.lower()}" for s, n in by_sev.most_common()) or "nothing"
        print(f"- **{tool}**: {len(found)} findings in the tree ({counts}).")
        if added is not None:
            on_pr += [(tool,) + f for f in found if f[1] in added.get(f[0], ())]
    if added is None:
        return 0
    print()
    if not on_pr:
        print("None on the lines this PR adds.")
        return 0
    print(f"{len(on_pr)} on the lines this PR adds:\n")
    print("| Tool | Where | Rule | Severity | Finding |")
    print("|---|---|---|---|---|")
    for tool, path, line, rule, sev, msg in sorted(on_pr, key=lambda f: (f[1], f[2])):
        print(f"| {tool} | `{path}:{line}` | {rule} | {sev.lower()} | {cell(msg)} |")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
