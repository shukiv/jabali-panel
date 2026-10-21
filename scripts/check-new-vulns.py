#!/usr/bin/env python3
"""check-new-vulns.py BASE.json HEAD.json

Compares two `osv-scanner scan source --format json` reports, of the PR's
base and of its head, and prints a Markdown summary for the job summary.

An advisory is new when the head has it for a package the base does not:
the PR added or changed that dependency. Exits 1 when a new one is rated 7.0
or more (high or critical), or has no rating at all, since Go's advisories
carry none and would otherwise never fail. Advisories already on the base
are listed, not counted.
"""
import json
import sys

FAIL_AT = 7.0


def advisories(path):
    """{(ecosystem, package, id): (version, severity or None, aliases)}

    OSV groups the ids one vulnerability has in different databases (GO-,
    GHSA-, CVE-); a group is one entry, under its first id in sort order.
    """
    with open(path) as f:
        report = json.load(f)
    out = {}
    for result in report.get("results") or []:
        for pkg in result.get("packages") or []:
            p = pkg.get("package", {})
            for group in pkg.get("groups") or []:
                try:
                    sev = float(group.get("max_severity") or "")
                except ValueError:
                    sev = None
                ids = sorted(group.get("ids") or [])
                if ids:
                    out[(p.get("ecosystem", ""), p.get("name", ""), ids[0])] = (p.get("version", ""), sev, ids[1:])
    return out


def rating(sev):
    return "none" if sev is None else f"{sev:.1f}"


def table(rows):
    lines = ["| Package | Version | Advisory | Also | Rating |", "|---|---|---|---|---|"]
    for (eco, name, vid), (version, sev, aliases) in sorted(rows.items()):
        lines.append(f"| {eco} `{name}` | {version} | [{vid}](https://osv.dev/{vid}) | {', '.join(aliases)} | {rating(sev)} |")
    return "\n".join(lines)


def main(argv):
    if len(argv) != 3:
        print(__doc__.strip(), file=sys.stderr)
        return 2
    base, head = advisories(argv[1]), advisories(argv[2])
    new = {k: v for k, v in head.items() if k not in base}
    known = {k: v for k, v in head.items() if k in base}
    failing = {k: v for k, v in new.items() if v[1] is None or v[1] >= FAIL_AT}

    print("### New dependency vulnerabilities (OSV-Scanner)\n")
    if not new:
        print("This PR adds no known vulnerability.\n")
    else:
        print(f"This PR adds {len(new)} known vulnerabilit{'y' if len(new) == 1 else 'ies'}:\n")
        print(table(new) + "\n")
        if failing:
            print(f"{len(failing)} rated {FAIL_AT} or more, or not rated: update the dependency to a fixed version.\n")
    if known:
        print(f"<details><summary>{len(known)} already on the base branch (not counted)</summary>\n")
        print(table(known) + "\n")
        print("</details>")
    return 1 if failing else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
