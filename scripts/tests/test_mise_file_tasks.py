"""File-task structure/discovery regressions; never executes a live task."""

import json
import os
from pathlib import Path
import shutil
import stat
import subprocess
import tomllib
import unittest


ROOT = Path(__file__).resolve().parents[2]
TASKS = {
    "test:tasks": "test/tasks.sh",
    "test:network": "test/network.sh",
    "test:soak": "test/soak.sh",
    "test:alsa:loopback": "test/alsa/loopback.sh",
    "test:alsa:play3": "test/alsa/play3.sh",
    "test:alsa:e2e": "test/alsa/e2e/_default.sh",
    "test:alsa:e2e:virtual": "test/alsa/e2e/virtual.sh",
}


class FileTasksTest(unittest.TestCase):
    def test_task_directory_contains_only_real_file_tasks(self):
        for path in (ROOT / "mise-tasks").rglob("*"):
            if not path.is_file():
                continue
            with self.subTest(path=path.relative_to(ROOT)):
                text = path.read_text()
                self.assertIn('#MISE description=', text)
                self.assertIn(path.suffix, [".sh", ".ps1"])
                if path.suffix == ".sh":
                    self.assertTrue(text.startswith("#!/"))
                    if os.name != "nt":
                        self.assertTrue(path.stat().st_mode & stat.S_IXUSR)
                elif os.name != "nt":
                    self.assertFalse(path.stat().st_mode & stat.S_IXUSR,
                                     "PowerShell prepare must not duplicate POSIX prepare")

    def test_file_tasks_have_no_duplicate_toml_wrappers(self):
        config = tomllib.loads((ROOT / "mise.toml").read_text())
        for name, relative in TASKS.items():
            with self.subTest(task=name):
                self.assertNotIn(name, config["tasks"])
                self.assertTrue((ROOT / "mise-tasks" / relative).is_file())

    @unittest.skipUnless(shutil.which("mise"), "mise not installed")
    def test_mise_discovers_file_sources_dependencies_and_subdirectory_cwd(self):
        for name, relative in TASKS.items():
            with self.subTest(task=name):
                # Metadata only: not `mise run`; no prepare/audio/mixer work.
                result = subprocess.run(
                    ["mise", "tasks", "info", name, "--json"],
                    cwd=ROOT / "audio", capture_output=True, text=True,
                    timeout=30, check=True,
                )
                info = json.loads(result.stdout)
                expected = ROOT / "mise-tasks" / relative
                self.assertEqual(Path(info["file"]).resolve(), expected.resolve())
                self.assertEqual(Path(info["source"]).resolve(), expected.resolve())
                self.assertEqual(info["depends"], ["prepare"])
                self.assertEqual(info["run"], [])
                self.assertEqual(Path(info["dir"]).resolve(), ROOT.resolve())


if __name__ == "__main__":
    unittest.main(verbosity=2)
