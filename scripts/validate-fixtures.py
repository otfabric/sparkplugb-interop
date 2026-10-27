#!/usr/bin/env python3
"""Validates every fixtures/<name>/fixture.json against the schema and the
rules of docs/FIXTURES.md that a JSON schema cannot express.

Usage: validate-fixtures.py [--require-schema] [FILE...]
"""

import json
import pathlib
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
SCHEMA = ROOT / "fixtures" / "schema" / "sparkplugb-fixture.schema.json"
MAX_SAFE = 2**53 - 1
RANGES = {
    "Int8": (-(2**7), 2**7 - 1),
    "Int16": (-(2**15), 2**15 - 1),
    "Int32": (-(2**31), 2**31 - 1),
    "Int64": (-MAX_SAFE, MAX_SAFE),
    "UInt8": (0, 2**8 - 1),
    "UInt16": (0, 2**16 - 1),
    "UInt32": (0, 2**32 - 1),
    "UInt64": (0, MAX_SAFE),
    "DateTime": (0, MAX_SAFE),
}
RESERVED = ("Node Control/", "bdSeq")


def check_value(where, datatype, value, errors):
    if datatype in RANGES:
        lo, hi = RANGES[datatype]
        if isinstance(value, bool) or not isinstance(value, int) or not lo <= value <= hi:
            errors.append(f"{where}: {value!r} is not a {datatype} in [{lo}, {hi}]")
    elif datatype in ("Float", "Double"):
        if isinstance(value, bool) or not isinstance(value, (int, float)):
            errors.append(f"{where}: {value!r} is not a number")
        elif datatype == "Float":
            import struct

            if struct.unpack("f", struct.pack("f", value))[0] != value:
                errors.append(f"{where}: {value!r} is not exactly representable as a Float")
    elif datatype == "Boolean":
        if not isinstance(value, bool):
            errors.append(f"{where}: {value!r} is not a boolean")
    elif not isinstance(value, str):
        errors.append(f"{where}: {value!r} is not a string")


def check_rules(path, fx):
    errors = []
    if fx["name"] != path.parent.name:
        errors.append(f"name {fx['name']!r} is not the directory name {path.parent.name!r}")
    aliases = {}
    scopes = {"node": fx["node"]["metrics"]}
    device_ids = []
    for d in fx["devices"]:
        if d["id"] in device_ids:
            errors.append(f"device {d['id']!r} appears twice")
        device_ids.append(d["id"])
        scopes[f"device {d['id']}"] = d["metrics"]
    datatypes = {}
    for scope, metrics in scopes.items():
        names = set()
        for m in metrics:
            where = f"{scope}, metric {m['name']!r}"
            if m["name"] in names:
                errors.append(f"{where}: name appears twice")
            names.add(m["name"])
            if m["name"].startswith(RESERVED):
                errors.append(f"{where}: names under Node Control/ and bdSeq are the edge's own")
            if m["alias"] in aliases:
                errors.append(f"{where}: alias {m['alias']} is already used by {aliases[m['alias']]}")
            aliases[m["alias"]] = where
            check_value(where, m["datatype"], m["value"], errors)
            datatypes[(scope, m["name"])] = m["datatype"]
    for i, u in enumerate(fx["updates"]):
        if not u["node"] and not u["devices"]:
            errors.append(f"updates[{i}] changes nothing")
        changes = [("node", c) for c in u["node"]]
        for dev, cs in u["devices"].items():
            if dev not in device_ids:
                errors.append(f"updates[{i}]: unknown device {dev!r}")
                continue
            changes += [(f"device {dev}", c) for c in cs]
        seen = set()
        for scope, c in changes:
            where = f"updates[{i}], {scope}, metric {c['name']!r}"
            if (scope, c["name"]) in seen:
                errors.append(f"{where}: changed twice")
            seen.add((scope, c["name"]))
            dt = datatypes.get((scope, c["name"]))
            if dt is None:
                errors.append(f"{where}: no such metric")
            else:
                check_value(where, dt, c["value"], errors)
    return errors


def main():
    args = sys.argv[1:]
    require = "--require-schema" in args
    files = [pathlib.Path(a) for a in args if not a.startswith("--")]
    if not files:
        files = sorted(p for p in (ROOT / "fixtures").glob("*/fixture.json"))
    try:
        import jsonschema
    except ImportError:
        if require:
            print("jsonschema is not installed", file=sys.stderr)
            return 2
        jsonschema = None
    schema = json.loads(SCHEMA.read_text())
    status = 0
    for path in files:
        try:
            fx = json.loads(path.read_text())
        except (OSError, ValueError) as e:
            print(f"FAIL {path}: {e}")
            status = 1
            continue
        errors = []
        if jsonschema is not None:
            v = jsonschema.Draft202012Validator(schema)
            errors += [f"schema: {'/'.join(map(str, e.path))}: {e.message}" for e in v.iter_errors(fx)]
        if not errors:
            errors += check_rules(path, fx)
        for e in errors:
            print(f"FAIL {path.relative_to(ROOT) if path.is_absolute() else path}: {e}")
        if errors:
            status = 1
        else:
            print(f"ok   {path.relative_to(ROOT) if path.is_absolute() else path}")
    return status


if __name__ == "__main__":
    sys.exit(main())
