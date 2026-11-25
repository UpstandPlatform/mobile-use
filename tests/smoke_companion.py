"""Exercise a paired companion on a disposable Android 16 emulator."""

import argparse
import json
import re
import subprocess
import time
import urllib.request
import uuid
import xml.etree.ElementTree as ET
from pathlib import Path


SERIAL = "emulator-5580"


def adb(*args: str) -> bytes:
    return subprocess.check_output(["adb", "-s", SERIAL, *args], timeout=10)


def button(*names: str, timeout: float = 10) -> tuple[int, int] | None:
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        try:
            adb("shell", "uiautomator", "dump", "/data/local/tmp/mobile-use-qa.xml")
            root = ET.fromstring(adb("exec-out", "cat", "/data/local/tmp/mobile-use-qa.xml"))
        except (subprocess.SubprocessError, ET.ParseError):
            continue
        for node in root.iter("node"):
            if not any(node.get(key, "").casefold() in names for key in ("text", "content-desc")):
                continue
            rect = [int(value) for value in re.findall(r"\d+", node.get("bounds", ""))]
            if len(rect) == 4:
                return (rect[0] + rect[2]) // 2, (rect[1] + rect[3]) // 2
    return None


def tap(*names: str, optional: bool = False, timeout: float = 10) -> bool:
    point = button(*names, timeout=timeout)
    if point is None:
        if optional:
            return False
        raise AssertionError(f"Cannot find a button for {names}")
    adb("shell", "input", "tap", str(point[0]), str(point[1]))
    return True


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("origin")
    parser.add_argument("credentials", type=Path)
    parser.add_argument("--camera", action="store_true")
    args = parser.parse_args()
    credentials = json.loads(args.credentials.read_text(encoding="utf-8-sig"))

    def api(role, path, body=None, method="GET"):
        request = urllib.request.Request(args.origin + path, None if body is None else json.dumps(body).encode(), {
            "Content-Type": "application/json", "Authorization": f"Bearer {credentials[role + '_token']}",
        }, method=method)
        with urllib.request.urlopen(request, timeout=10) as response:
            return json.load(response)

    assert adb("shell", "getprop", "ro.kernel.qemu").strip() == b"1", "This test requires a disposable emulator"
    adb("shell", "am", "force-stop", "org.mobileuse.companion")
    adb("shell", "am", "start", "-n", "org.mobileuse.companion/.MainActivity")
    devices = []
    for _ in range(12):
        devices = [item for item in api("admin", "/v1/devices") if item["mode"] == "companion" and item["online"]]
        if devices:
            break
        time.sleep(0.7)
    assert len(devices) == 1, "Pair the emulator companion and keep its Activity open"
    for previous in api("admin", "/v1/sessions"):
        if previous["device_id"] == devices[0]["id"]:
            api("admin", f"/v1/sessions/{previous['id']}/end", method="POST")
    caps = ["screen", "microphone", "camera"]
    api("admin", f"/v1/devices/{devices[0]['id']}", {
        "label": "Companion verification", "owner": "local-owner", "profile": "personal",
        "grants": [{"subject": "local-agent", "capabilities": caps}],
    }, "PUT")
    session = api("agent", "/v1/sessions", {"device_id": devices[0]["id"], "capabilities": caps, "purpose": "Verify Android companion"}, "POST")
    base = f"/v1/sessions/{session['id']}"

    def operation(kind, **payload):
        return api("agent", base + "/operations", {"kind": kind, **payload}, "POST")

    def wait_for(operation_fn, field):
        for _ in range(12):
            result = operation_fn()
            if result.get(field):
                return result[field]
            time.sleep(0.35)
        raise AssertionError(f"Companion payload unavailable: {result.get('error')}")

    ended_on_phone = False
    try:
        tap("approve")
        tap("share screen")
        tap("allow", optional=True, timeout=3)
        tap("share one app", timeout=8)
        tap("share entire screen")
        tap("start now", "start sharing", "share screen", "share", "next")
        shot = wait_for(lambda: operation("observe", observe={"screen": True, "tree": False}), "observation")
        assert shot["image"]["mime"] == "image/jpeg"
        voice = operation("sensor_request", id=uuid.uuid4().hex, sensor={"type": "microphone", "duration_seconds": 2})["sensor"]
        tap("record clip")
        tap("while using the app", "only this time", optional=True, timeout=5)
        clip = wait_for(lambda: operation("artifact_get", sensor={"type": "microphone", "request_id": voice["id"]}), "artifact")
        assert clip["mime"] == "audio/wav" and len(clip["data"]) == 85392
        if args.camera:
            sensor = operation("sensor_request", id=uuid.uuid4().hex, sensor={"type": "camera"})["sensor"]
            tap("open camera")
            tap("while using the app", "only this time", optional=True, timeout=5)
            tap("shutter", "take photo", "take picture")
            tap("done", "use photo", "confirm", "save", "ok", "accept", "use picture")
            photo = wait_for(lambda: operation("artifact_get", sensor={"type": "camera", "request_id": sensor["id"]}), "artifact")
            assert photo["mime"] == "image/jpeg"
        tap("stop")
        for _ in range(12):
            if api("agent", base)["state"] == "ended":
                ended_on_phone = True
                break
            time.sleep(0.4)
        else:
            raise AssertionError("The phone's Stop control did not end the session")
    finally:
        if not ended_on_phone:
            api("admin", base + "/end", method="POST")
    checks = "approval, Android screen projection, microphone" + (", camera" if args.camera else "")
    print(f"Passed companion {checks}, and on-device stop")


if __name__ == "__main__":
    main()
