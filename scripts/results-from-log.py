#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Turn what scripts/interop.sh recorded into the organisation's interop-result-v1.

    scripts/results-from-log.py <work dir> <results.json> <started> <reproduce command>

The work directory of a run holds results.log (one tab-separated line per
result: id, outcome, roles as "role=name" words, milliseconds, reason, known
bug), implementations.tsv (name, version, image; "-" for the version of versions.yaml),
active (the known bugs this run can judge) and reproduced (those that showed). A
known bug is named by the first seven columns of tests/known-bugs.tsv.

The exit status is 1 when the run failed: a fail or infra-error, a known bug
that no longer reproduces, or no pass at all (scripts/interop_result.py).
"""

import json
import re
import sys
from pathlib import Path

import interop_result

ROOT = Path(__file__).resolve().parent.parent
REPOSITORY = "otfabric/sparkplugb-interop"
OUTCOMES = set(interop_result.OUTCOMES)


def lines(path):
    return path.read_text().splitlines() if path.exists() else []


def load_waivers(path=ROOT / "tests" / "known-bugs.tsv"):
    """first seven columns -> waiver: tests/known-bugs.tsv has implementation,
    version, role, peer, broker, fixture, step, description and evidence URL."""
    waivers = {}
    for line in lines(path):
        if not line.strip() or line.startswith("#") or line.startswith("implementation\t"):
            continue
        f = line.split("\t")
        key = " ".join(f[:7])
        name = re.sub(r"[^a-z0-9]+", "-", "-".join(f[:7]).lower().replace(".*", "any")).strip("-")
        waivers[key] = {"id": name, "scope": "%s@%s" % (f[2], f[0]), "evidence": f[8]}
    return waivers


def pins(path=ROOT / "versions.yaml"):
    """implementation or broker -> version, revision and licence, from versions.yaml."""
    out, name, inside = {}, None, False
    for line in lines(path):
        if re.match(r"^\S", line):
            inside = line.startswith(("implementations:", "brokers:"))
        m = re.match(r"^  ([A-Za-z0-9._-]+):\s*$", line)
        if inside and m:
            name = m.group(1)
            out[name] = {}
        m = re.match(r'^    (version|revision|license): "?([^"#]+?)"?\s*(#.*)?$', line)
        if inside and name and m:
            out[name][m.group(1)] = m.group(2)
    return out


def parse_result(line, waivers):
    rid, outcome, roles, ms, reason, bug = line.split("\t")
    if outcome not in OUTCOMES:
        raise SystemExit("%s: unknown outcome %r" % (rid, outcome))
    parts = rid.split(".")
    res = {"id": rid, "suite": parts[0], "roles": dict(r.split("=", 1) for r in roles.split()),
           "outcome": outcome, "durationMs": int(ms)}
    if len(parts) > 1:
        res["feature"] = parts[1]
    if outcome != "pass":
        res["reason"] = reason or outcome
    if outcome == "known-difference":
        if bug not in waivers:
            raise SystemExit("%s: a known bug without a line in tests/known-bugs.tsv: %s" % (rid, bug))
        res["waiver"] = waivers[bug]["id"]
    return res


def implementations(rows, versions, digest=interop_result.repo_digest):
    out = []
    for row in rows:
        name, version, image = row.split("\t")
        i = dict(versions.get(name, {}))
        i.update({"name": name, "independent": True})
        if version != "-":
            i["version"] = version
        pinned = image if "@sha256:" in image else digest(image)
        if pinned:
            i["image"] = pinned
        out.append(i)
    return out or [{"name": "none", "version": "none"}]


def convert(work, started, finished, reproduce, waivers):
    active = lines(work / "active")
    reproduced = set(lines(work / "reproduced"))
    used = []
    for text in dict.fromkeys(active):
        if text not in waivers:
            raise SystemExit("a known bug without a line in tests/known-bugs.tsv: %s" % text)
        w = waivers[text]
        used.append({"id": w["id"], "scope": w["scope"], "evidence": w["evidence"], "reproduced": text in reproduced})
    return {"schemaVersion": "1.0", "repository": REPOSITORY,
            "interopVersion": interop_result.version_field(str(ROOT), "interop_version"),
            "contractVersion": interop_result.version_field(str(ROOT), "contract_version"),
            "gitSha": interop_result.git_sha(str(ROOT)), "startedAt": started, "finishedAt": finished,
            "host": interop_result.host(),
            "implementations": implementations(lines(work / "implementations.tsv"), pins()),
            "waivers": used, "reproduce": reproduce,
            "results": [parse_result(line, waivers) for line in lines(work / "results.log")]}


def main(argv):
    if len(argv) != 4:
        print(__doc__, file=sys.stderr)
        return 2
    work, out, started, reproduce = Path(argv[0]), argv[1], argv[2], argv[3]
    doc = convert(work, started, interop_result.now(), reproduce, load_waivers())
    interop_result.write(out, doc)
    bad = interop_result.schema_errors(doc) + interop_result.problems(doc)
    print("%s: %s" % (out, ", ".join("%d %s" % (doc["summary"][o], o) for o in interop_result.OUTCOMES)))
    for p in bad:
        print("FAIL " + p, file=sys.stderr)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
