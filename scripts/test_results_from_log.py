#!/usr/bin/env python3
# SPDX-License-Identifier: MIT
"""Tests of scripts/results-from-log.py that need no Docker."""

import importlib.util
import os
import tempfile
import unittest
from pathlib import Path
from unittest import mock

import interop_result

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("results_from_log", os.path.join(HERE, "results-from-log.py"))
rl = importlib.util.module_from_spec(spec)
spec.loader.exec_module(rl)

ROLES = "host=a edge=b broker=m"
WAIVERS = {"the bug": {"id": "a-1-x", "scope": "edge@a", "evidence": "https://example.com/bug"}}


def line(rid, outcome, reason="", bug="", roles=ROLES):
    return "\t".join((rid, outcome, roles, "3", reason, bug))


def convert(log, active=(), reproduced=()):
    with tempfile.TemporaryDirectory() as d:
        work = Path(d)
        (work / "results.log").write_text("".join(x + "\n" for x in log))
        (work / "active").write_text("".join("%s\n" % x for x in active))
        (work / "reproduced").write_text("".join(x + "\n" for x in reproduced))
        (work / "implementations.tsv").write_text("a\t1\timage-a\nb\t2\timage-b\nmosquitto\t-\tbroker@sha256:%s\n" % ("0" * 64))
        with mock.patch.object(interop_result, "repo_digest", return_value=None):
            doc = rl.convert(work, "2026-10-30T00:00:00Z", "2026-10-30T00:00:01Z", "make interop", WAIVERS)
    doc["summary"] = interop_result.summarize(doc["results"])
    return doc


class Convert(unittest.TestCase):
    def test_a_pass_is_valid_and_passes(self):
        doc = convert([line("case.read.one", "pass")])
        self.assertEqual(interop_result.schema_errors(doc), [])
        self.assertEqual(interop_result.problems(doc), [])
        r = doc["results"][0]
        self.assertEqual((r["suite"], r["feature"], r["roles"], r["durationMs"]), ("case", "read", {"host": "a", "edge": "b", "broker": "m"}, 3))

    def test_no_result_is_no_pass(self):
        self.assertTrue(interop_result.problems(convert([])))

    def test_only_unsupported_fails(self):
        doc = convert([line("case.read.one", "unsupported", "needs a feature")])
        self.assertEqual(interop_result.schema_errors(doc), [])
        self.assertTrue(interop_result.problems(doc))

    def test_a_failure_keeps_its_reason(self):
        doc = convert([line("case.read.one", "pass"), line("case.read.two", "fail", "got 3")])
        self.assertEqual(doc["results"][1]["reason"], "got 3")
        self.assertTrue(interop_result.problems(doc))

    def test_infra_error_fails(self):
        self.assertTrue(interop_result.problems(convert([line("case.read.one", "pass"), line("harness.stopped", "infra-error", "exit 1")])))

    def test_known_difference_has_its_waiver(self):
        doc = convert([line("case.read.one", "pass"), line("case.read.two", "known-difference", "the bug", "the bug")],
                      active=["the bug"], reproduced=["the bug"])
        self.assertEqual(interop_result.schema_errors(doc), [])
        self.assertEqual(interop_result.problems(doc), [])
        self.assertEqual(doc["results"][1]["waiver"], "a-1-x")

    def test_stale_waiver_fails(self):
        doc = convert([line("case.read.one", "pass")], active=["the bug"])
        self.assertFalse(doc["waivers"][0]["reproduced"])
        self.assertTrue(interop_result.problems(doc))

    def test_a_broker_takes_its_version_from_versions_yaml(self):
        doc = convert([line("case.read.one", "pass")])
        broker = [i for i in doc["implementations"] if i["name"] == "mosquitto"][0]
        self.assertTrue(broker["version"])
        self.assertTrue(broker["image"].endswith("0" * 64))

    def test_known_difference_needs_a_waiver(self):
        with self.assertRaises(SystemExit):
            convert([line("case.read.one", "known-difference", "other", "other")])

    def test_active_bug_needs_a_waiver(self):
        with self.assertRaises(SystemExit):
            convert([line("case.read.one", "pass")], active=["other"])

    def test_unknown_outcome_is_refused(self):
        with self.assertRaises(SystemExit):
            convert([line("case.read.one", "skip")])


class Files(unittest.TestCase):
    def test_every_known_bug_loads(self):
        waivers = rl.load_waivers()
        self.assertEqual(len({w["id"] for w in waivers.values()}), len(waivers))

    def test_pins_of_this_repository(self):
        self.assertTrue(rl.pins())


if __name__ == "__main__":
    unittest.main()
