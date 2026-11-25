#!/usr/bin/env bash
set -euo pipefail

: "${RELEASE_TAG:?Set the release tag}"
adb wait-for-device
adb install "release/mobile-use-companion-$RELEASE_TAG.apk"
adb install "release/mobile-use-control-$RELEASE_TAG.apk"
adb shell am start -W -n org.mobileuse.companion/.MainActivity | tee "$RUNNER_TEMP/companion-launch.txt"
grep -q '^Status: ok' "$RUNNER_TEMP/companion-launch.txt"
adb shell pidof org.mobileuse.companion

# A benign unsupported command checks the installed instrumentation entry point.
request=$(printf '%s' '{"type":"probe"}' | base64 -w 0 | tr '/+' '_-' | tr -d '=')
adb shell am instrument -w -e request "$request" org.mobileuse.control/.ControlInstrumentation \
    | tee "$RUNNER_TEMP/control-launch.txt"
grep -q 'mobile_use_result=unsupported' "$RUNNER_TEMP/control-launch.txt"
