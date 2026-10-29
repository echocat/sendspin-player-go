"""Hardware-free PTY tests. Every mixer/audio/Go command is replaced by a fake.

Run: python3 mise-tasks/tests/test_play3_low_level_loopback.py
No host audio configuration, audio device or microphone is accessed.
"""

import json
import os
import pty
import select
import shutil
import signal
import sys
import tempfile
import time
import unittest
from pathlib import Path

FAKE = r'''
import json, os, pathlib, shutil, signal, sys, time
tool = pathlib.Path(sys.argv[0]).name
args = sys.argv[1:]
root = pathlib.Path(os.environ["FAKE_ROOT"])
fault = os.environ.get("FAKE_FAULT", "")
def event(kind):
    with (root / "events.jsonl").open("a") as f:
        f.write(json.dumps([tool, kind, args]) + "\n")
event("start")
statepath = root / "state.json"
state = json.loads(statepath.read_text())
def save(): statepath.write_text(json.dumps(state))
if tool == "go":
    shutil.copyfile(__file__, args[args.index("-o") + 1])
    os.chmod(args[args.index("-o") + 1], 0o700)
elif tool == "uv": pass
elif tool == "loopback":
    if args[0] == "generate":
        path = pathlib.Path(args[args.index("-out") + 1])
        path.write_bytes(b"SYNTHETIC ONLY")
        pathlib.Path(str(path) + ".json").write_text('{}')
    else:
        aligned = os.environ.get("FAKE_CONNECTED") == "1" and fault != "analysis"
        pathlib.Path(args[args.index("-out") + 1]).write_text(json.dumps({"status":"fake-aligned" if aligned else "fake-inconclusive"}))
        sys.exit(0 if aligned else 1)
elif tool == "alsactl":
    path = pathlib.Path(args[args.index("--file") + 1])
    if "store" in args:
        if fault == "store": sys.exit(1)
        path.write_text(json.dumps(state))
    else:
        if fault == "restore":
            statepath.write_text(path.read_text())  # partial restore enabled monitoring
            sys.exit(1)
        statepath.write_text(path.read_text())
elif tool == "amixer":
    args = args[args.index("--") + 1:]
    if args[0] == "sset":
        name, direction, value = args[1:]
        key = name + direction
        if value == "mute": state[key + "switch"] = "off"
        elif value in ["unmute", "cap"]: state[key + "switch"] = "on"
        else: state[key + "gain"] = int(value.removesuffix("dB"))
        save()
        if fault == "partial" and value == "-40dB": sys.exit(1)
        if fault == "unmute" and value == "unmute": sys.exit(1)
    elif args[0] == "sget":
        name = args[1]
        for channel in ["Front Left", "Front Right"]:
            text = channel + ":"
            for direction in (["Playback"] if name == "Speaker" else ["Playback", "Capture"]):
                key = name + direction.lower()
                gain = state[key + "gain"]
                if fault == "readback" and name == "Speaker": gain = -20
                if direction == "Capture" and fault == "capture-too-high": gain = 25
                if direction == "Capture" and fault == "capture-not-applied": gain = 0
                text += f" {direction} 24 [50%] [{gain:.2f}dB] [{state[key + 'switch']}]"
            print(text)
    elif args[0] == "info": print("SYNTHETIC card / USB041e:324d" if fault != "identity" else "SYNTHETIC other card")
    else: print(json.dumps(state, sort_keys=True))
elif tool in ["arecord", "aplay", "sdk-playback"]:
    def stopped(*_):
        event("stopped")
        sys.exit(143)
    signal.signal(signal.SIGTERM, stopped)
    if fault == "record" and tool == "arecord": sys.exit(1)
    if fault == "play" and tool in ["aplay", "sdk-playback"]: sys.exit(1)
    if tool == "sdk-playback":
        assert pathlib.Path(os.environ["SENDSPIN_ALSA_REFERENCE_FILE"]).name == "reference.wav"
        if os.environ.get("SENDSPIN_ALSA_E2E_PLAYBACK") == "1":
            assert args == ["-test.run", "^TestReferenceServerPlaybackScenario$", "-test.v", "-test.timeout=250s"]
            assert os.environ["SENDSPIN_ALSA_E2E_VIRTUAL"] == ""
            assert pathlib.Path(os.environ["SENDSPIN_ALSA_E2E_ROOT"]).is_absolute()
            pathlib.Path(os.environ["SENDSPIN_ALSA_E2E_ARTIFACT_DIR"], "e2e-report.json").write_text('{"status":"fake-only"}')
            if fault == "e2e-hang": time.sleep(60)
        else:
            assert os.environ["SENDSPIN_ALSA_REFERENCE_PLAYBACK"] == "1"
            assert args == ["-test.run", "^TestHardwareWeakReferencePlayback$", "-test.v", "-test.timeout=5s"]
    if tool == "arecord":
        sys.stdout.buffer.write(b"SYNTHETIC ONLY")
        time.sleep(60 if fault == "record-timeout" else 2)
    elif fault == "xrun": print("underrun!!!", file=sys.stderr)
    event("completed")
elif tool == "sleep": time.sleep(.01)
else: raise AssertionError("unexpected fake " + tool)
'''


@unittest.skipUnless(sys.platform == "linux", "Linux Bash/PTY harness only")
class SmokeHarnessTest(unittest.TestCase):
    def run_harness(self, fault="", cancel=False, confirm_disconnect=True, acknowledge=True, capture_24db=False, extra_args=(), connected=False, terminal=True):
        # The entire script/repo/var/tool environment is temporary, not the real repo.
        preferred_tmp = "/tmp/opencode" if Path("/tmp/opencode").is_dir() else None
        with tempfile.TemporaryDirectory(prefix="play3-fakes-", dir=preferred_tmp) as tmp:
            root = Path(tmp)
            (root / "mise-tasks").mkdir()
            script = root / "mise-tasks" / "play3-low-level-loopback.sh"
            shutil.copyfile(Path(__file__).resolve().parents[1] / script.name, script)
            tools = root / "fake-bin"
            tools.mkdir()
            fake = tools / "fake"
            fake.write_text(f"#!{sys.executable}\n" + FAKE)
            fake.chmod(0o700)
            for tool in ["go", "uv", "amixer", "alsactl", "arecord", "aplay", "sleep"]:
                (tools / tool).symlink_to(fake)
            before = {
                "Speakerplaybackgain": -20, "Speakerplaybackswitch": "on",
                # Start with an enabled monitor to test disconnect-before-restore.
                "Micplaybackgain": 16, "Micplaybackswitch": "off" if connected and fault != "monitor-active" else "on",
                "Miccapturegain": 24, "Miccaptureswitch": "on",
            }
            (root / "state.json").write_text(json.dumps(before))
            write_fd = None
            if terminal:
                pid, fd = pty.fork()
            else:
                fd, write_fd = os.pipe()
                pid = os.fork()
            if pid == 0:
                if not terminal:
                    os.close(fd)
                    null = os.open(os.devnull, os.O_RDONLY)
                    os.dup2(null, 0)
                    os.close(null)
                    os.dup2(write_fd, 1)
                    os.dup2(write_fd, 2)
                    os.close(write_fd)
                os.environ.update(FAKE_ROOT=str(root), FAKE_FAULT=fault,
                                  FAKE_CONNECTED="1" if connected else "0",
                                  PATH=str(tools) + os.pathsep + os.environ["PATH"])
                args = ["bash"]
                if os.environ.get("PLAY3_FAKE_TRACE") == "1": args.append("-x")
                args.append(str(script))
                if acknowledge: args.append("--acknowledge-unverified-wiring")
                if capture_24db: args.append("--capture-gain-24db")
                if connected: args.append("--connected-leave-muted")
                args.extend(extra_args)
                os.execvp("bash", args)
            if write_fd is not None: os.close(write_fd)
            output = b""
            answered = set()
            cancel_sent = False
            deadline = time.monotonic() + 20
            result = None
            try:
                while time.monotonic() < deadline:
                    ready, _, _ = select.select([fd], [], [], .1)
                    if ready:
                        try:
                            data = os.read(fd, 65536)
                        except OSError:
                            break
                        if not data: break
                        output += data
                    for prompt, answer in [
                        (b"Type VORBEREITEN.", b"VORBEREITEN\n"),
                        (b"Type VERBUNDEN to run once.", b"VERBUNDEN\n"),
                        (b"otherwise settings stay muted).", b"ABGEZOGEN\n" if confirm_disconnect else b"NEIN\n"),
                    ]:
                        if prompt in output and prompt not in answered:
                            self.assertTrue(terminal, "unexpected prompt without a terminal")
                            if prompt.startswith(b"Type VERBUNDEN") and cancel is True:
                                answer = b"\x03"
                            if prompt.startswith(b"otherwise"):
                                events_path = root / "events.jsonl"
                                events = [json.loads(s) for s in events_path.read_text().splitlines()]
                                self.assertFalse(any(e[0] == "alsactl" and "restore" in e[2] for e in events))
                            os.write(fd, answer)
                            answered.add(prompt)
                    if cancel in ["recording", "e2e"] and not cancel_sent:
                        events_path = root / "events.jsonl"
                        target = "sdk-playback" if cancel == "e2e" else "arecord"
                        if events_path.exists() and any(json.loads(s)[0] == target for s in events_path.read_text().splitlines()):
                            os.write(fd, b"\x03")
                            cancel_sent = True
                else:
                    self.fail("fake harness hung: " + output.decode(errors="replace") + "\nFAKE EVENTS:\n" + (root / "events.jsonl").read_text())
                _, result = os.waitpid(pid, 0)
                events_path = root / "events.jsonl"
                events = [json.loads(s) for s in events_path.read_text().splitlines()] if events_path.exists() else []
                state = json.loads((root / "state.json").read_text())
                # Every audio command must select the exact explicit hw device.
                for event in events:
                    if event[0] in ["arecord", "aplay"]:
                        self.assertIn("hw:CARD=S3,DEV=0", event[2])
                return os.waitstatus_to_exitcode(result), output.decode(errors="replace"), events, state, before
            finally:
                os.close(fd)
                if result is None:
                    os.kill(pid, signal.SIGKILL)
                    os.waitpid(pid, 0)

    def test_success_with_inconclusive_analysis_restores_after_disconnect(self):
        code, output, events, state, before = self.run_harness()
        self.assertEqual(code, 0, output)
        self.assertEqual(state, before)
        self.assertIn("No automatic gain increase", output)
        self.assertEqual(sum(e[0] == "aplay" and e[1] == "start" for e in events), 1)
        self.assertEqual(sum(e[0] == "arecord" and e[1] == "start" for e in events), 1)
        self.assertTrue(any(e[0] == "amixer" and e[2][-3:] == ["Mic", "capture", "0dB"] for e in events))
        self.assertFalse(any(e[0] == "amixer" and e[2][-3:] == ["Mic", "capture", "24dB"] for e in events))

    def test_explicit_24db_mode_keeps_output_and_duration_unchanged(self):
        code, output, events, state, before = self.run_harness(capture_24db=True)
        self.assertEqual(code, 0, output)
        self.assertEqual(state, before)
        self.assertIn("Requested capture gain: 24dB", output)
        self.assertTrue(any(e[0] == "amixer" and e[2][-3:] == ["Mic", "capture", "24dB"] for e in events))
        self.assertEqual([e[2][-1] for e in events if e[0] == "amixer" and e[2][-3:-1] == ["Speaker", "playback"] and e[2][-1].endswith("dB")], ["-40dB"])
        generate = next(e[2] for e in events if e[0] == "loopback" and e[2][0] == "generate")
        self.assertEqual(generate[generate.index("-amplitude") + 1], "0.01")
        self.assertEqual(generate[generate.index("-seconds") + 1], "3")
        record = [e[2] for e in events if e[0] == "arecord" and e[1] == "start"]
        self.assertEqual(len(record), 1)
        self.assertEqual(record[0][record[0].index("--duration") + 1], "5")
        self.assertEqual(sum(e[0] == "aplay" and e[1] == "start" for e in events), 1)

    def test_24db_mode_requires_exact_capture_readback_and_restores_on_abort(self):
        for fault in ["capture-too-high", "capture-not-applied"]:
            with self.subTest(fault=fault):
                code, output, events, state, before = self.run_harness(fault=fault, capture_24db=True)
                self.assertNotEqual(code, 0, output)
                self.assertEqual(state, before)
                self.assertFalse(any(e[0] in ["arecord", "aplay"] for e in events))

    def test_arbitrary_gain_options_fail_before_tool_access(self):
        for args in [("--capture-gain-48db",), ("--capture-gain-24db", "--capture-gain-24db"), ("--sdk-output",), ("--sdk-output", "--sdk-output")]:
            with self.subTest(args=args):
                code, _, events, state, before = self.run_harness(extra_args=args)
                self.assertNotEqual(code, 0)
                self.assertFalse(events)
                self.assertEqual(state, before)

    def test_sdk_mode_replaces_aplay_and_builds_before_mixer_changes(self):
        code, output, events, state, before = self.run_harness(capture_24db=True, extra_args=("--sdk-output",))
        self.assertEqual(code, 0, output)
        self.assertEqual(state, before)
        self.assertFalse(any(e[0] == "aplay" for e in events))
        sdk = [i for i,e in enumerate(events) if e[0] == "sdk-playback" and e[1] == "start"]
        self.assertEqual(len(sdk), 1)
        build = next(i for i,e in enumerate(events) if e[0] == "go" and "test" in e[2])
        change = next(i for i,e in enumerate(events) if e[0] == "amixer" and "sset" in e[2])
        self.assertLess(build, change)

    def test_sdk_output_failure_stops_capture_and_restores(self):
        code, output, events, state, before = self.run_harness(fault="play", capture_24db=True, extra_args=("--sdk-output",))
        self.assertNotEqual(code, 0, output)
        self.assertEqual(state, before)
        self.assertFalse(any(e[0] == "aplay" for e in events))
        self.assertTrue(any(e[0] == "arecord" and e[1] == "stopped" for e in events))

    def test_connected_mode_has_no_prompts_or_tty_and_leaves_muted(self):
        code, output, events, state, _ = self.run_harness(capture_24db=True, extra_args=("--sdk-output",), connected=True, terminal=False)
        self.assertEqual(code, 0, output)
        self.assertEqual(state["Speakerplaybackswitch"], "off")
        self.assertEqual(state["Micplaybackswitch"], "off")
        self.assertEqual(state["Speakerplaybackgain"], -40)
        self.assertEqual(state["Miccapturegain"], 24)
        self.assertFalse(any(e[0] == "alsactl" and "restore" in e[2] for e in events))
        self.assertEqual(sum(e[0] == "sdk-playback" and e[1] == "start" for e in events), 1)
        for prompt in ["VORBEREITEN", "VERBUNDEN", "ABGEZOGEN"]: self.assertNotIn(prompt, output)

    def test_connected_failures_exit_without_prompt_and_leave_muted(self):
        for fault in ["play", "analysis", "capture-too-high"]:
            with self.subTest(fault=fault):
                code, output, events, state, _ = self.run_harness(fault=fault, capture_24db=True, extra_args=("--sdk-output",), connected=True, terminal=False)
                self.assertNotEqual(code, 0, output)
                self.assertEqual(state["Speakerplaybackswitch"], "off")
                self.assertEqual(state["Micplaybackswitch"], "off")
                self.assertFalse(any(e[0] == "alsactl" and "restore" in e[2] for e in events))

    def test_connected_active_monitor_aborts_without_changes_or_audio(self):
        code, output, events, state, before = self.run_harness(fault="monitor-active", capture_24db=True, extra_args=("--sdk-output",), connected=True, terminal=False)
        self.assertNotEqual(code, 0, output)
        self.assertEqual(state, before)
        self.assertFalse(any(e[0] in ["arecord", "aplay", "sdk-playback"] for e in events))
        self.assertFalse(any(e[0] == "amixer" and "sset" in e[2] for e in events))

    def test_connected_ctrl_c_stops_capture_and_leaves_muted(self):
        code, output, events, state, _ = self.run_harness(cancel="recording", capture_24db=True, extra_args=("--sdk-output",), connected=True)
        self.assertEqual(code, 130, output)
        self.assertEqual(state["Speakerplaybackswitch"], "off")
        self.assertEqual(state["Micplaybackswitch"], "off")
        self.assertFalse(any(e[0] == "alsactl" and "restore" in e[2] for e in events))

    def test_e2e_mode_uses_independent_scenario_without_capture_or_prompts(self):
        code, output, events, state, _ = self.run_harness(capture_24db=True, extra_args=("--sdk-output", "--e2e"), connected=True, terminal=False)
        self.assertEqual(code, 0, output)
        self.assertNotIn("VORBEREITEN", output)
        self.assertNotIn("ABGEZOGEN", output)
        self.assertFalse(any(e[0] in ["arecord", "aplay"] for e in events))
        self.assertEqual(sum(e[0] == "sdk-playback" and e[1] == "start" for e in events), 1)
        setup = next(i for i,e in enumerate(events) if e[0] == "uv")
        change = next(i for i,e in enumerate(events) if e[0] == "amixer" and "sset" in e[2])
        self.assertLess(setup, change)
        self.assertEqual(state["Speakerplaybackswitch"], "off")
        self.assertEqual(state["Micplaybackswitch"], "off")

    def test_e2e_failure_still_mutes_without_capture_or_restore(self):
        code, output, events, state, _ = self.run_harness(fault="play", capture_24db=True, extra_args=("--sdk-output", "--e2e"), connected=True, terminal=False)
        self.assertNotEqual(code, 0, output)
        self.assertFalse(any(e[0] in ["arecord", "aplay"] for e in events))
        self.assertEqual(state["Speakerplaybackswitch"], "off")
        self.assertEqual(state["Micplaybackswitch"], "off")

    def test_e2e_ctrl_c_stops_player_without_capture_or_prompts(self):
        code, output, events, state, _ = self.run_harness(fault="e2e-hang", cancel="e2e", capture_24db=True, extra_args=("--sdk-output", "--e2e"), connected=True)
        self.assertEqual(code, 130, output)
        self.assertTrue(any(e[0] == "sdk-playback" and e[1] == "stopped" for e in events))
        self.assertFalse(any(e[0] in ["arecord", "aplay"] for e in events))
        self.assertEqual(state["Speakerplaybackswitch"], "off")
        self.assertEqual(state["Micplaybackswitch"], "off")

    def test_opt_in_flag_is_required_before_any_tool_access(self):
        code, _, events, state, before = self.run_harness(acknowledge=False)
        self.assertNotEqual(code, 0)
        self.assertFalse(events)
        self.assertEqual(state, before)

    def test_failures_restore_and_never_escalate(self):
        for fault in ["identity", "store", "partial", "readback", "record", "unmute", "play", "xrun", "record-timeout"]:
            with self.subTest(fault=fault):
                code, output, events, state, before = self.run_harness(fault=fault)
                self.assertNotEqual(code, 0, output)
                self.assertEqual(state, before)
                if fault in ["identity", "store", "partial", "readback"]:
                    self.assertFalse(any(e[0] in ["aplay", "arecord"] for e in events))
                self.assertLessEqual(sum(e[0] == "aplay" and e[1] == "start" for e in events), 1)

    def test_ctrl_c_at_connection_prompt_does_not_open_audio(self):
        code, output, events, state, before = self.run_harness(cancel=True)
        self.assertEqual(code, 130, output)
        self.assertEqual(state, before)
        self.assertFalse(any(e[0] in ["aplay", "arecord"] for e in events))

    def test_no_disconnect_confirmation_leaves_muted_not_restored(self):
        code, output, events, state, _ = self.run_harness(confirm_disconnect=False)
        self.assertNotEqual(code, 0, output)
        self.assertEqual(state["Speakerplaybackswitch"], "off")
        self.assertEqual(state["Micplaybackswitch"], "off")
        self.assertFalse(any(e[0] == "alsactl" and "restore" in e[2] for e in events))

    def test_ctrl_c_during_capture_reaps_before_restoring(self):
        for capture_24db in [False, True]:
            with self.subTest(capture_24db=capture_24db):
                code, output, events, state, before = self.run_harness(cancel="recording", capture_24db=capture_24db)
                self.assertEqual(code, 130, output)
                self.assertEqual(state, before)
                stopped = next(i for i, e in enumerate(events) if e[0] == "arecord" and e[1] == "stopped")
                restored = next(i for i, e in enumerate(events) if e[0] == "alsactl" and "restore" in e[2])
                self.assertLess(stopped, restored)

    def test_partial_restore_failure_mutes_again(self):
        code, output, _, state, _ = self.run_harness(fault="restore")
        self.assertNotEqual(code, 0, output)
        self.assertIn("RESTORE FAILED", output)
        self.assertEqual(state["Speakerplaybackswitch"], "off")
        self.assertEqual(state["Micplaybackswitch"], "off")


if __name__ == "__main__":
    unittest.main(verbosity=2)
