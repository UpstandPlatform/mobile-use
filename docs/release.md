# Release and artifact verification

The Release workflow runs on a `vMAJOR.MINOR.PATCH` tag reachable from `main`. It reruns CI, builds the artifacts from the tagged commit, and verifies the downloadable files before publishing. A failed run can be rerun from the same tag. The workflow declines to replace a published GitHub release.

## Published artifacts

| Artifact | Contents |
| --- | --- |
| `mobile-use-<tag>-<os>-<arch>` | CLI, local service, connector, MCP server, and embedded console for Windows, Linux, or macOS |
| `mobile-use-companion-<tag>.apk` | Android 14+ companion signed with the project’s release key |
| `mobile-use-control-<tag>.apk` | Optional signed Android helper for authorized ADB control |
| `ghcr.io/upstandplatform/mobile-use:<tag>` | Linux AMD64 and ARM64 control-plane image with a BuildKit SBOM and provenance |
| `mobile-use-<tag>-oci-linux-<arch>.tar.gz` | Offline Docker image archive |
| Deployment bundle and Helm chart | Compose, PostgreSQL initialization, TURN, OIDC example, and Kubernetes manifests |
| SDK bundle and Python wheel | TypeScript and Python clients, with the HTTP API specification in the SDK bundle |
| `SHA256SUMS` and `oci-image.txt` | SHA-256 checksums and the immutable registry reference |

The workflow installs both signed APKs on an Android 16 emulator, confirms the companion starts, and invokes the helper’s instrumentation entry point. It runs the Linux AMD64 image and checks the registry manifest for both target platforms. CI covers Windows, Linux, and macOS plus the Android build, web build, and restricted PostgreSQL role.

## Cut a release

Push and verify the intended commit on `main`. Create an annotated tag and push it:

```sh
git tag -a v0.1.0 -m "mobile-use v0.1.0"
git push origin v0.1.0
```

Tags map to increasing Android version codes using `major × 1,000,000 + minor × 1,000 + patch + 1`. After the workflow succeeds, authorized users can download the release from GitHub. The OCI registry uses the exact release tag and digest. The private repository and package retain their configured access permissions.

## Android signing

The release job uses the `ANDROID_KEYSTORE_BASE64` and `ANDROID_SIGNING_PASSWORD` GitHub Actions secrets. The PKCS#12 keystore uses the `mobile-use-release` alias and the store and key use the same password. Take a recovery backup of the keystore and password in the organization’s credential vault. Continue using the existing key for updates to devices with these APKs installed.

The signing job checks both signed APKs against the pinned public certificate in `deploy/android-release-certificate.pem`. To check a downloaded file using Android SDK Build Tools, run:

```sh
apksigner verify --print-certs mobile-use-companion-v0.1.0.apk
openssl x509 -in deploy/android-release-certificate.pem -noout -fingerprint -sha256
```

Compare the SHA-256 certificate digests after ignoring case and colon separators. For offline images and platform archives, verify `SHA256SUMS` before use. The desktop archives use checksums and the tagged GitHub build. Windows Authenticode and Apple Developer ID notarization require separate release identities.
