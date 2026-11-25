"""Verify the public API against an enrolled, disposable Android emulator."""

import argparse
import base64
import json
import time
import urllib.error
import urllib.request
import uuid
from pathlib import Path


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("origin")
    parser.add_argument("credentials", type=Path)
    parser.add_argument("--connector", default="local")
    parser.add_argument("--helper", action="store_true")
    parser.add_argument("--browser-fixture", action="store_true")
    parser.add_argument("--maintenance-fixture", action="store_true")
    args = parser.parse_args()
    credentials = json.loads(args.credentials.read_text(encoding="utf-8-sig"))

    def request(role, path, method="GET", body=None, expected=200):
        data = json.dumps(body).encode() if body is not None else None
        headers = {
            "Content-Type": "application/json",
            "Authorization": f"Bearer {credentials[role + '_token']}",
        }
        req = urllib.request.Request(args.origin + path, data, headers, method=method)
        try:
            response = urllib.request.urlopen(req, timeout=24)
        except urllib.error.HTTPError as error:
            response = error
        with response:
            result = json.load(response)
            if response.status != expected:
                raise AssertionError(f"{method} {path}: {response.status}, {result.get('error')}")
            return result

    devices = request("admin", "/v1/devices")
    emulators = [d for d in devices if d["virtual"] and d["online"] and d["connector"] == args.connector]
    assert len(emulators) == 1, "Connect exactly one disposable emulator with the selected connector"
    emulator = emulators[0]
    if args.maintenance_fixture:
        verify_maintenance(request, emulator)
    capabilities = ["screen", "ui_tree", "touch", "text", "apps", "browser"]
    if args.helper:
        assert emulator["capabilities"].get("multi_touch"), "Install and enable the Android control helper first"
        capabilities.append("multi_touch")
    request("admin", f"/v1/devices/{emulator['id']}", "PUT", {
        "owner": "local-owner", "label": "Android verification", "profile": "personal",
        "grants": [{"subject": "local-agent", "capabilities": capabilities}],
    })
    session = request("agent", "/v1/sessions", "POST", {
        "device_id": emulator["id"], "purpose": "Verify Android Settings on the emulator", "capabilities": capabilities,
    }, 201)
    base = f"/v1/sessions/{session['id']}"
    assert session["state"] == "pending_approval"
    request("agent", base + "/approve", "POST", expected=403)
    request("admin", base + "/approve", "POST")

    def operation(kind, allow_error=False, **data):
        request("admin", base + "/presence", "POST")
        result = request("agent", base + "/operations", "POST", {"kind": kind, **data})
        if not allow_error:
            assert result["state"] == "succeeded", result.get("error")
        return result

    def observe(*, screen, tree, apps=()):
        end = time.monotonic() + 12
        while True:
            result = operation("observe", allow_error=True, observe={"screen": screen, "tree": tree})
            observation = result.get("observation")
            if observation and (not apps or observation.get("app_id") in apps) and (not tree or observation.get("nodes")):
                return observation
            if time.monotonic() > end:
                raise AssertionError(f"Foreground observation did not settle: {(observation or {}).get('app_id')}, {result.get('error')}")
            time.sleep(0.4)

    try:
        apps = operation("apps_list")["apps"]
        assert "com.android.settings" in apps
        operation("app_terminate", id=uuid.uuid4().hex, app_id="com.android.settings")
        operation("app_launch", id=uuid.uuid4().hex, app_id="com.android.settings")
        time.sleep(0.8)
        settings_apps = ("com.android.settings", "com.google.android.settings.intelligence")
        shot = observe(screen=True, tree=True, apps=settings_apps)
        assert base64.b64decode(shot["image"]["data"]).startswith(b"\x89PNG")
        assert len(shot["nodes"]) > 5
        if args.helper:
            operation("app_terminate", id=uuid.uuid4().hex, app_id="org.mobileuse.fixture")
            operation("app_launch", id=uuid.uuid4().hex, app_id="org.mobileuse.fixture")
            shot = observe(screen=True, tree=True, apps=("org.mobileuse.fixture",))
            search = next(node for node in shot["nodes"] if node.get("role", "").endswith("EditText"))
            operation("act", id=uuid.uuid4().hex, action={"type": "tap", "frame_id": shot["frame_id"], "node_id": search["id"]})
            time.sleep(0.7)
            for _ in range(5):
                target = observe(screen=False, tree=True, apps=("org.mobileuse.fixture",))
                if any(node.get("focused") and node.get("role", "").endswith("EditText") for node in target.get("nodes", [])):
                    break
                time.sleep(0.35)
            else:
                raise AssertionError("The Android fixture field did not receive input focus")
            text = "Résumé 東京 " + uuid.uuid4().hex[:5]
            operation("act", id=uuid.uuid4().hex, action={"type": "type", "frame_id": target["frame_id"], "text": text})
            shot = observe(screen=True, tree=True)
            assert any(text in (node.get("label") or "") for node in shot["nodes"]), "Unicode input missing from the native field"
            operation("act", id=uuid.uuid4().hex, action={"type": "pinch", "frame_id": shot["frame_id"],
                "point": {"x": 0.27, "y": 0.28}, "end": {"x": 0.2, "y": 0.22},
                "second_point": {"x": 0.65, "y": 0.48}, "second_end": {"x": 0.74, "y": 0.51}})
            shot = observe(screen=True, tree=False)
        cmd = {"type": "key", "frame_id": shot["frame_id"], "key": "home"}
        operation("act", id="home-" + uuid.uuid4().hex, action=cmd)
        stale = operation("act", allow_error=True, id="stale-" + uuid.uuid4().hex, action=cmd)
        assert stale["error"]["code"] == "STALE_REFERENCE", stale.get("error")
        time.sleep(0.8)
        screen = observe(screen=True, tree=False)
        assert screen["app_id"] != shot["app_id"], screen["app_id"]
        apps_tree = observe(screen=False, tree=True)
        assert apps_tree["nodes"]
        request("admin", base + "/pause", "POST")
        request("agent", base + "/operations", "POST", {"kind": "observe", "observe": {"screen": True, "tree": False}}, 409)
        request("admin", base + "/resume", "POST")
        operation("observe", observe={"screen": True, "tree": False})
        if args.browser_fixture:
            operation("app_launch", id=uuid.uuid4().hex, app_id="org.mobileuse.fixture")
            observe(screen=True, tree=False, apps=("org.mobileuse.fixture",))
            for _ in range(5):
                contexts = operation("browser_contexts").get("contexts", [])
                target = next((context for context in contexts if "mobile-use" in context.get("title", "").lower()), None)
                if target:
                    break
                time.sleep(0.4)
            assert target, "Inspectable Android WebView context was unavailable"
            doc = operation("browser_observe", browser={"context_id": target["id"]})["page"]
            base_action = {"context_id": target["id"], "document_id": doc["document_id"]}
            operation("browser_act", id=uuid.uuid4().hex, browser={**base_action, "type": "fill", "selector": "#sample", "text": "Verified DOM"})
            operation("browser_act", id=uuid.uuid4().hex, browser={**base_action, "type": "click", "selector": "#apply"})
            result = operation("browser_observe", browser={"context_id": target["id"]})["page"]
            assert any(node.get("id") == "result" and node.get("text") == "Verified DOM" for node in result["elements"])
    finally:
        request("admin", base + "/end", "POST")
    request("agent", base + "/operations", "POST", {"kind": "observe", "observe": {"screen": True, "tree": False}}, 409)
    extras = ", Unicode input and pinch" if args.helper else ""
    if args.browser_fixture:
        extras += ", WebView DOM inspection and actions"
    print(f"Passed Android launch, tree, screenshot, Home, stale frame, pause/resume and revocation{extras} via {args.connector}")


def verify_maintenance(request, emulator):
    package = "org.mobileuse.fixture"
    artifact = next((item for item in emulator.get("catalog", []) if item["app_id"] == package), None)
    assert artifact is not None, "Configure the mobile-use test fixture APK in the Android catalog"
    capabilities = ["apps", "touch"]
    enrollment = {"owner": "local-owner", "label": "Android verification", "profile": "dedicated",
                  "grants": [{"subject": "local-agent", "capabilities": capabilities}]}
    path = f"/v1/devices/{emulator['id']}"
    request("admin", path, "PUT", enrollment)

    def start(actor):
        session = request(actor, "/v1/sessions", "POST", {
            "device_id": emulator["id"], "purpose": "Verify approved test fixture maintenance", "capabilities": capabilities,
        }, 201)
        return f"/v1/sessions/{session['id']}"

    def maintenance(base, actor, verb, *, confirmed=True, expected=200):
        return request(actor, base + "/operations", "POST", {
            "kind": "app_" + verb, "id": uuid.uuid4().hex,
            "maintenance": {"artifact_id": artifact["id"], "confirm": f"{verb} {artifact['id']}" if confirmed else "skip"},
        }, expected)

    base = start("agent")
    try:
        maintenance(base, "agent", "reset", expected=403)
    finally:
        request("admin", base + "/end", "POST")
    base = start("admin")
    try:
        maintenance(base, "admin", "install", confirmed=False, expected=400)
        for verb in ("install", "reset"):
            result = maintenance(base, "admin", verb)
            assert result["state"] == "succeeded", result.get("error")
        result = request("admin", base + "/operations", "POST", {"kind": "apps_list"})
        assert package in result.get("apps", []), result
    finally:
        request("admin", base + "/end", "POST")
    request("admin", path, "PUT", {**enrollment, "profile": "personal"})
    base = start("admin")
    try:
        request("admin", base + "/approve", "POST")
        maintenance(base, "admin", "reset", expected=403)
    finally:
        request("admin", base + "/end", "POST")
    print("Passed dedicated Android fixture install and reset, including confirmation and role checks")


if __name__ == "__main__":
    main()
