#!/usr/bin/env python3
"""Validate the documents the self-tests kept against the contract schemas.

Usage: validate-documents.py <directory>

Run the self-tests with REPORT_DIR set to keep every document:

    REPORT_DIR=reports make smoke interop
    python3 scripts/validate-documents.py reports

results.json is an interop-result-v1 document, capabilities-*.json a capability
document, call-*.json a call result and every line of *.host.jsonl an event of
a host. Needs the jsonschema package.
"""

import json
import sys
from pathlib import Path

import jsonschema

ROOT = Path(__file__).resolve().parent.parent


def validator(name):
    schema = json.loads((ROOT / "schemas" / name).read_text())
    jsonschema.Draft202012Validator.check_schema(schema)
    return jsonschema.Draft202012Validator(schema)


def check(v, doc, where):
    errors = 0
    for e in v.iter_errors(doc):
        path = "/".join(str(x) for x in e.absolute_path) or "(root)"
        print(f"FAIL {where}: {path}: {e.message}")
        errors += 1
    return errors


def main(argv):
    if len(argv) != 1:
        print(__doc__, file=sys.stderr)
        return 2
    kinds = {
        "interop results": (validator("interop-result-v1.schema.json"), "results.json"),
        "capabilities": (validator("capabilities.schema.json"), "capabilities-*.json"),
        "call results": (validator("call-result.schema.json"), "call-*.json"),
    }
    events = validator("event.schema.json")
    counts = {k: 0 for k in kinds} | {"events": 0}
    failed = 0
    base = Path(argv[0])
    for kind, (v, pattern) in kinds.items():
        for path in sorted(base.glob(pattern)):
            try:
                doc = json.loads(path.read_text())
            except ValueError as e:
                print(f"FAIL {path.name}: not JSON: {e}")
                failed += 1
                continue
            counts[kind] += 1
            failed += check(v, doc, path.name)
    for path in sorted(base.glob("*.host.jsonl")):
        for n, line in enumerate(path.read_text().splitlines(), 1):
            if not line.strip():
                continue
            try:
                doc = json.loads(line)
            except ValueError as e:
                print(f"FAIL {path.name}:{n}: not JSON: {e}")
                failed += 1
                continue
            counts["events"] += 1
            failed += check(events, doc, f"{path.name}:{n}")
    print(", ".join(f"{n} {k}" for k, n in counts.items()) + f", {failed} errors")
    if counts["capabilities"] == 0 or counts["call results"] == 0:
        print("no capability documents or no call results found", file=sys.stderr)
        return 1
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
