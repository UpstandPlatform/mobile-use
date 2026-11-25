#!/usr/bin/env bash
set -euo pipefail

: "${ANDROID_HOME:?Set ANDROID_HOME to the Android SDK}"
: "${ANDROID_KEYSTORE_BASE64:?Configure the Android release keystore}"
: "${ANDROID_SIGNING_PASSWORD:?Configure the Android release signing password}"
: "${RELEASE_TAG:?Set the release tag}"

tools="$ANDROID_HOME/build-tools/36.0.0"
umask 077
keystore=$(mktemp)
trap 'rm -f -- "$keystore"' EXIT
printf '%s' "$ANDROID_KEYSTORE_BASE64" | tr -d '\r\n' | base64 --decode > "$keystore"
unset ANDROID_KEYSTORE_BASE64
export ANDROID_SIGNING_PASSWORD="$(printf '%s' "$ANDROID_SIGNING_PASSWORD" | tr -d '\r\n')"
expected=$(openssl x509 -in deploy/android-release-certificate.pem -outform DER | sha256sum | cut -d ' ' -f 1)
mkdir -p release

for app in companion control; do
    input="android/$app/build/outputs/apk/release/$app-release-unsigned.apk"
    output="release/mobile-use-$app-$RELEASE_TAG.apk"
    "$tools/zipalign" -P 16 -f 4 "$input" "$output"
    "$tools/apksigner" sign --ks "$keystore" --ks-type PKCS12 --ks-key-alias mobile-use-release \
        --ks-pass env:ANDROID_SIGNING_PASSWORD --key-pass env:ANDROID_SIGNING_PASSWORD "$output"
    verified=$("$tools/apksigner" verify --print-certs "$output")
    actual=$(awk '/^Signer #1 certificate SHA-256 digest: / {print $NF}' <<< "$verified")
    if [[ "$actual" != "$expected" ]]; then
        echo "The $app APK did not match the pinned release certificate." >&2
        exit 1
    fi
    "$tools/zipalign" -c -P 16 4 "$output"
done
