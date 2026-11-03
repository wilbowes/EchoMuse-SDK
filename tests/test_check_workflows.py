"""The workflow guard must catch each thing it claims to, or it passes vacuously."""

import importlib.util
import tempfile
import unittest
from pathlib import Path

spec = importlib.util.spec_from_file_location(
    "check_workflows", Path(__file__).resolve().parents[1] / "tools" / "check_workflows.py")
cw = importlib.util.module_from_spec(spec)
spec.loader.exec_module(cw)

GOOD = """name: x
on: [push]
permissions:
  contents: read
jobs:
  a:
    runs-on: ubuntu-24.04
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
      - uses: ./local-action
"""


def errors_for(text: str) -> list[str]:
    with tempfile.TemporaryDirectory() as d:
        p = Path(d) / "wf.yml"
        p.write_text(text)
        return cw.check(p)


class CheckWorkflows(unittest.TestCase):
    def test_good_passes(self):
        self.assertEqual(errors_for(GOOD), [])

    def test_tag_ref_fails(self):
        self.assertTrue(errors_for(GOOD.replace(
            "@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1", "@v7")))

    def test_sha_only_in_comment_fails(self):
        self.assertTrue(errors_for(GOOD.replace(
            "@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1",
            "@v7 # 3d3c42e5aac5ba805825da76410c181273ba90b1")))

    def test_docker_tag_fails_digest_passes(self):
        self.assertTrue(errors_for(GOOD + "      - uses: docker://alpine:3.20\n"))
        self.assertEqual(errors_for(GOOD + "      - uses: docker://alpine@sha256:" + "a" * 64 + "\n"), [])

    def test_latest_runner_fails(self):
        self.assertTrue(errors_for(GOOD.replace("ubuntu-24.04", "ubuntu-latest")))

    def test_missing_permissions_fails(self):
        self.assertTrue(errors_for(GOOD.replace("permissions:\n  contents: read\n", "")))

    def test_job_level_permissions_do_not_count_as_default(self):
        text = GOOD.replace("permissions:\n  contents: read\n", "").replace(
            "    runs-on", "    permissions:\n      contents: read\n    runs-on")
        self.assertTrue(errors_for(text))

    def test_pull_request_target_fails(self):
        self.assertTrue(errors_for(GOOD.replace("on: [push]", "on: [push, pull_request_target]")))
        self.assertTrue(errors_for(GOOD.replace("on: [push]", "on:\n  pull_request_target:")))


if __name__ == "__main__":
    unittest.main()
