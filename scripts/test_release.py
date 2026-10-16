#!/usr/bin/env python3
"""Verify real cross-built release archives with Python's standard library."""
import hashlib
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import tarfile
import tempfile
import textwrap
import unittest
import zipfile

ROOT = Path(__file__).resolve().parents[1]
SCRIPT = ROOT / "scripts" / "package-release.sh"
TARGETS = {
    "linux_amd64": ("linux", "amd64"),
    "linux_arm64": ("linux", "arm64"),
    "linux_armv7": ("linux", "arm"),
    "darwin_amd64": ("darwin", "amd64"),
    "darwin_arm64": ("darwin", "arm64"),
    "windows_amd64": ("windows", "amd64"),
    "windows_arm64": ("windows", "arm64"),
}


class PublicationTests(unittest.TestCase):
    def run_publication(self, scenario, prerelease=False):
        workflow = (ROOT / ".github" / "workflows" / "release.yml").read_text()
        step = workflow.split("      - name: Publish GitHub Release\n", 1)[1]
        script = textwrap.dedent(step.split("        run: |\n", 1)[1])
        with tempfile.TemporaryDirectory() as directory:
            workspace = Path(directory)
            tools = workspace / "tools"
            tools.mkdir()
            gh = tools / "gh"
            gh.write_text(textwrap.dedent("""\
                #!/usr/bin/env python3
                import json, os, pathlib, sys
                args = sys.argv[1:]
                root = pathlib.Path(os.environ['RUNNER_TEMP'])
                with (root / 'calls.jsonl').open('a') as log:
                    log.write(json.dumps(args) + '\\n')
                scenario = os.environ['PUBLICATION_SCENARIO']
                if args[0] == 'api':
                    changed = scenario == 'tag_before' or (scenario == 'tag_during' and (root / 'uploaded').exists())
                    print('changed' if changed else 'expected')
                elif args[:2] == ['release', 'view']:
                    if scenario == 'published':
                        print(json.dumps({'isDraft': False}))
                    elif scenario == 'draft':
                        print(json.dumps({'isDraft': True}))
                    else:
                        sys.exit(1)
                elif args[:2] == ['release', 'upload']:
                    (root / 'uploaded').touch()
                    if scenario == 'upload_failure':
                        sys.exit(1)
                """))
            jq = tools / "jq"
            jq.write_text("#!/usr/bin/env python3\nimport json, sys\nprint(str(json.load(open(sys.argv[-1]))['isDraft']).lower())\n")
            gh.chmod(0o755)
            jq.chmod(0o755)
            path = workspace / "publish.sh"
            path.write_text(script)
            (workspace / "dist").mkdir()
            (workspace / "dist" / "speedprobe_test.tar.gz").touch()
            (workspace / "dist" / "checksums.txt").touch()
            env = dict(os.environ, PATH=str(tools) + os.pathsep + os.environ["PATH"],
                       RUNNER_TEMP=directory, GITHUB_REPOSITORY="example/SpeedProbe",
                       RELEASE_TAG="v0.0.0-test", EXPECTED_TAG_OBJECT="expected",
                       PRERELEASE=str(prerelease).lower(), PUBLICATION_SCENARIO=scenario)
            result = subprocess.run(["bash", "-e", "-o", "pipefail", str(path)], cwd=workspace,
                                    env=env, capture_output=True, text=True)
            calls = [json.loads(line) for line in (workspace / "calls.jsonl").read_text().splitlines()]
            return result, calls

    def test_new_release_and_draft_retry(self):
        for scenario in ("new", "draft"):
            with self.subTest(scenario=scenario):
                result, calls = self.run_publication(scenario)
                self.assertEqual(result.returncode, 0, result.stderr)
                release_calls = [call for call in calls if call[0] == "release"]
                self.assertEqual(any(call[1] == "create" for call in release_calls), scenario == "new")
                self.assertEqual([call[1] for call in release_calls[-2:]], ["upload", "edit"])
                self.assertIn("--draft=false", release_calls[-1])
                self.assertIn("--prerelease=false", release_calls[-1])

    def test_prerelease_and_published_release(self):
        result, calls = self.run_publication("new", prerelease=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn("--prerelease=true", calls[-1])
        self.assertIn("--latest=false", calls[-1])
        result, calls = self.run_publication("published")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertFalse(any(call[:2] in (["release", "upload"], ["release", "edit"], ["release", "create"]) for call in calls))

    def test_failed_upload_or_changed_tag_is_never_published(self):
        for scenario in ("upload_failure", "tag_before", "tag_during"):
            with self.subTest(scenario=scenario):
                result, calls = self.run_publication(scenario)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse(any(call[:2] == ["release", "edit"] for call in calls))


class ReleaseTests(unittest.TestCase):
    def test_version_validation(self):
        for tag in ("v0.1.0", "v1.2.3-rc.1", "v2.0.0-beta"):
            result = subprocess.run(["bash", str(SCRIPT), "--validate-version", tag], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
        for tag in ("", "dev", "v1.2", "v1.2.3;echo unsafe", "v1.2.3/../../bad", "v1.2.3\nother"):
            result = subprocess.run(["bash", str(SCRIPT), "--validate-version", tag], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0, tag)
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "must-not-exist"
            result = subprocess.run(["bash", str(SCRIPT), "dev", str(output)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)
            self.assertFalse(output.exists())

    def test_real_archives_and_checksums(self):
        tag = "v0.0.0-test"
        # A separate source tree avoids modifying user files and lets the test
        # prove that private files alongside source are never packaged.
        with tempfile.TemporaryDirectory(prefix="speedprobe release ") as directory:
            workspace = Path(directory) / "source"
            workspace.mkdir()
            for name in ("cmd", "internal", "deploy"):
                shutil.copytree(ROOT / name, workspace / name)
            (workspace / "scripts").mkdir()
            shutil.copy2(SCRIPT, workspace / "scripts" / SCRIPT.name)
            for name in ("go.mod", "README.md", "LICENSE", ".env.example"):
                shutil.copy2(ROOT / name, workspace / name)
            (workspace / "docs").mkdir()
            shutil.copy2(ROOT / "docs" / "guide.md", workspace / "docs" / "guide.md")
            (workspace / "docs" / "private-notes.md").write_text("never-package-this\n")
            (workspace / ".env").write_text("SPEEDPROBE_TOKEN=never-package-this\n")
            (workspace / ".speedprobe-tokens").write_text("never-package-this\n")
            output = Path(directory) / "output"
            subprocess.run(["bash", str(workspace / "scripts" / SCRIPT.name), tag, str(output)], check=True)
            checksums = {}
            for row in (output / "checksums.txt").read_text().splitlines():
                digest, name = row.split(maxsplit=1)
                checksums[name.lstrip("*")] = digest
            self.assertEqual(len(checksums), len(TARGETS))
            host_os = {"Darwin": "darwin", "Linux": "linux", "Windows": "windows"}.get(platform.system())
            host_arch = {"x86_64": "amd64", "AMD64": "amd64", "arm64": "arm64", "aarch64": "arm64"}.get(platform.machine())
            for target, (goos, goarch) in TARGETS.items():
                with self.subTest(target=target):
                    suffix = ".zip" if goos == "windows" else ".tar.gz"
                    archive = output / f"speedprobe_{tag}_{target}{suffix}"
                    self.assertEqual(hashlib.sha256(archive.read_bytes()).hexdigest(), checksums[archive.name])
                    binary = "speedprobe.exe" if goos == "windows" else "speedprobe"
                    expected = {binary, "README.md", "LICENSE", ".env.example", "deploy/nginx.conf", "docs/guide.md"}
                    if suffix == ".zip":
                        with zipfile.ZipFile(archive) as bundle:
                            self.assertEqual(set(bundle.namelist()), expected)
                            self.assertEqual(bundle.read("docs/guide.md"), (ROOT / "docs" / "guide.md").read_bytes())
                            executable = bundle.read(binary)
                    else:
                        with tarfile.open(archive) as bundle:
                            self.assertEqual(set(bundle.getnames()), expected)
                            self.assertEqual(bundle.extractfile("docs/guide.md").read(), (ROOT / "docs" / "guide.md").read_bytes())
                            self.assertTrue(bundle.getmember(binary).mode & 0o111)
                            executable = bundle.extractfile(binary).read()
                    path = Path(directory) / binary
                    path.write_bytes(executable)
                    info = subprocess.check_output(["go", "version", "-m", str(path)], text=True)
                    self.assertIn(f"GOOS={goos}", info)
                    self.assertIn(f"GOARCH={goarch}", info)
                    self.assertIn("CGO_ENABLED=0", info)
                    if goarch == "arm":
                        self.assertIn("GOARM=7", info)
                    if goos == host_os and goarch == host_arch:
                        os.chmod(path, 0o755)
                        self.assertEqual(subprocess.check_output([str(path), "version"], text=True).strip(), "SpeedProbe " + tag)
            self.assertEqual(len(list(output.iterdir())), len(TARGETS) + 1)


if __name__ == "__main__":
    unittest.main()
