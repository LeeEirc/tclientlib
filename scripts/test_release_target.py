#!/usr/bin/env python3
"""Regression tests for release entry points and GitHub output boundaries.

Run with: python3 -m unittest discover -s scripts -p 'test_release_target.py'
Only temporary local files and subprocesses are used; no tags or releases are
created by this suite.
"""

import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

from release_target import resolve_tag


SCRIPT = Path(__file__).with_name("release_target.py").resolve()


class ReleaseTargetTests(unittest.TestCase):
    def setUp(self):
        workspace = tempfile.TemporaryDirectory()
        self.addCleanup(workspace.cleanup)
        self.workspace = Path(workspace.name)
        self.version_file = self.workspace / "VERSION"

    def resolve(self, event, ref, requested=""):
        return resolve_tag(event, ref, requested, self.version_file)

    def write_version(self, content):
        self.version_file.write_bytes(content.encode("utf-8"))

    def run_script(self, event, ref, requested=""):
        env = os.environ.copy()
        env.update(
            GITHUB_EVENT_NAME=event,
            GITHUB_REF=ref,
            RELEASE_INPUT_TAG=requested,
        )
        return subprocess.run(
            [sys.executable, str(SCRIPT)],
            cwd=self.workspace,
            env=env,
            text=True,
            capture_output=True,
            timeout=5,
            check=False,
        )

    def test_checked_in_version_is_a_valid_release_target(self):
        # Reject an invalid version in the actual PR, not just in fixtures.
        version_file = Path(__file__).resolve().parents[1] / "VERSION"
        resolve_tag("push", "refs/heads/master", "", version_file)

    def test_master_push_uses_version_from_the_selected_checkout(self):
        self.write_version("v0.1.0\n")
        self.assertEqual(
            self.resolve("push", "refs/heads/master", "v9.9.9"), "v0.1.0"
        )

    def test_manual_release_uses_explicit_input_without_reading_version(self):
        for version in (None, "invalid\nsecond line\n", "v9.9.9\n"):
            with self.subTest(version=version):
                if version is not None:
                    self.write_version(version)
                self.assertEqual(
                    self.resolve("workflow_dispatch", "refs/heads/master", "v0.1.0"),
                    "v0.1.0",
                )

    def test_tag_push_uses_the_ref_without_reading_version(self):
        for version in (None, "invalid\nsecond line\n", "v9.9.9\n"):
            with self.subTest(version=version):
                if version is not None:
                    self.write_version(version)
                self.assertEqual(
                    self.resolve("push", "refs/tags/v0.1.0-rc.1", "v9.9.9"),
                    "v0.1.0-rc.1",
                )

    def test_unapproved_events_and_refs_are_rejected(self):
        self.write_version("v0.1.0\n")
        cases = (
            ("pull_request", "refs/pull/13/merge"),
            ("pull_request_target", "refs/heads/master"),
            ("workflow_call", "refs/heads/master"),
            ("", "refs/heads/master"),
            ("push", "refs/heads/feature"),
            ("push", "refs/heads/master/child"),
            ("push", "refs/heads/v0.1.0"),
            ("push", "master"),
            ("push", ""),
            ("workflow_dispatch", "refs/heads/feature"),
            ("workflow_dispatch", "refs/tags/v0.1.0"),
            ("workflow_dispatch", "master"),
        )
        for event, ref in cases:
            with self.subTest(event=event, ref=ref):
                with self.assertRaises(ValueError):
                    self.resolve(event, ref, "v0.1.0")

    def test_supported_versions_work_through_all_three_entry_points(self):
        versions = (
            "v0.0.0",
            "v0.1.0",
            "v10.20.30",
            "v1.2.3-rc.1",
            "v1.2.3-0",
            "v1.2.3-alpha.beta.7",
            "v1.2.3-alpha-beta.01a",
        )
        for tag in versions:
            with self.subTest(tag=tag):
                self.write_version(tag + "\n")
                self.assertEqual(self.resolve("push", "refs/heads/master"), tag)
                self.assertEqual(self.resolve("push", "refs/tags/" + tag), tag)
                self.assertEqual(
                    self.resolve("workflow_dispatch", "refs/heads/master", tag), tag
                )

    def test_invalid_versions_cannot_enter_through_an_alternative_trigger(self):
        versions = (
            "",
            "0.1.0",
            "V0.1.0",
            "v1.2",
            "v1.2.3.4",
            "v01.2.3",
            "v1.02.3",
            "v1.2.03",
            "v1.2.3-01",
            "v1.2.3-rc.01",
            "v1.2.3-",
            "v1.2.3-.rc",
            "v1.2.3-rc.",
            "v1.2.3-rc..1",
            "v1.2.3-rc_1",
            "v1.2.3+build.1",
            " v1.2.3",
            "v1.2.3 ",
            "v1.2.3\t",
            "v1.２.3",
            "v١.2.3",
            "v1.2.3-候选.1",
            "v1.2.3/extra",
            "v1.2.3; echo unexpected",
            "v1.2.3$(echo unexpected)",
            "v1.2.3\ncreate_tag=true",
            "v1.2.3\rcreate_tag=true",
        )
        for tag in versions:
            self.write_version(tag + "\n")
            for event, ref, requested in (
                ("push", "refs/heads/master", ""),
                ("push", "refs/tags/" + tag, ""),
                ("workflow_dispatch", "refs/heads/master", tag),
            ):
                with self.subTest(tag=tag, event=event, ref=ref):
                    with self.assertRaises(ValueError):
                        self.resolve(event, ref, requested)

    def test_version_file_accepts_normal_single_line_endings(self):
        for ending in ("", "\n", "\r\n"):
            with self.subTest(ending=repr(ending)):
                self.write_version("v0.1.0" + ending)
                self.assertEqual(self.resolve("push", "refs/heads/master"), "v0.1.0")

    def test_empty_or_multiline_version_files_are_rejected(self):
        for content in ("", "\n", "v0.1.0\n\n", "\nv0.1.0\n", "v0.1.0\nv0.2.0\n"):
            with self.subTest(content=content):
                self.write_version(content)
                with self.assertRaises(ValueError):
                    self.resolve("push", "refs/heads/master")

    def test_master_push_requires_a_version_file(self):
        with self.assertRaises(OSError):
            self.resolve("push", "refs/heads/master")

    def test_script_emits_only_one_output_assignment_for_each_entry_point(self):
        self.write_version("v0.1.0\r\n")
        for event, ref, requested in (
            ("push", "refs/heads/master", "v9.9.9"),
            ("push", "refs/tags/v0.1.0", "v9.9.9"),
            ("workflow_dispatch", "refs/heads/master", "v0.1.0"),
        ):
            with self.subTest(event=event, ref=ref):
                result = self.run_script(event, ref, requested)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(result.stdout, "tag=v0.1.0\n")
                self.assertEqual(result.stderr, "")

    def test_script_errors_do_not_write_partial_or_injected_outputs(self):
        self.write_version("v0.1.0\n")
        for event, ref, requested in (
            ("workflow_dispatch", "refs/heads/master", "v0.1.0\ntrusted=true"),
            ("workflow_dispatch", "refs/heads/master", ""),
            ("workflow_dispatch", "refs/heads/feature", "v0.1.0"),
            ("push", "refs/tags/v0.1.0\ntrusted=true", ""),
            ("pull_request", "refs/heads/master", "v0.1.0"),
        ):
            with self.subTest(event=event, ref=ref, requested=requested):
                result = self.run_script(event, ref, requested)
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stdout, "")
                self.assertTrue(result.stderr)

    def test_script_reports_missing_or_multiline_version_without_output(self):
        for content in (None, "v0.1.0\ntrusted=true\n"):
            with self.subTest(content=content):
                if content is not None:
                    self.write_version(content)
                result = self.run_script("push", "refs/heads/master")
                self.assertEqual(result.returncode, 1)
                self.assertEqual(result.stdout, "")
                self.assertTrue(result.stderr)


if __name__ == "__main__":
    unittest.main()
