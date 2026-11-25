# mobile-use

Control enrolled Android phones and iPhones from an agent or operator console. Run the service on your workstation, or connect authorized device hosts to your own private deployment. Agents connect through MCP or the HTTP API.

[Downloads](https://github.com/UpstandPlatform/mobile-use/releases) · [Deployment guide](docs/operations.md) · [Verification status](docs/verification.md)

```mermaid
flowchart LR
    Agent["MCP agent"] -->|"Sessions and actions"| Service["mobile-use service"]
    Console["Operator console"] -->|"Approval and viewing"| Service
    Connector["Device connector"] <-->|"Local or mutual TLS"| Service
    Connector -->|"Authorized ADB"| Android["Android"]
    Connector -->|"Appium on a Mac"| iPhone["iPhone"]
    Companion["Android companion"] <-->|"HTTPS and on-device consent"| Service
```

## Choose how to connect

| Goal | Install | Device requirements |
| --- | --- | --- |
| Android gestures, app actions, or WebView inspection | Desktop binary and Android SDK Platform Tools | Authorized USB or Wireless Debugging |
| Android screen sharing, camera stills, or microphone clips | Signed companion APK | Android 14+, an HTTPS endpoint the phone trusts, and on-device approval |
| iPhone automation | macOS binary, Xcode, Appium XCUITest, and signed WebDriverAgent | Mac trust and iPhone Developer Mode |
| Private remote control | Container or Helm chart, PostgreSQL, OIDC, and a device connector | Network access between the private service, agent, and connector |

## Install and start the desktop console

Download your platform archive from [Releases](https://github.com/UpstandPlatform/mobile-use/releases). Use `amd64` for Intel and AMD PCs and `arm64` for Apple Silicon or other ARM64 systems. This private repository and its packages require GitHub project access. Compare the download to its entry in `SHA256SUMS` using `Get-FileHash -Algorithm SHA256 <archive>` on Windows, `shasum -a 256 <archive>` on macOS, or `sha256sum <archive>` on Linux.

Extract the archive. From its folder, run `up` to initialize local credentials, start the workstation daemon with automatic local Wi-Fi companion TLS + mDNS, and open the desktop web console in your browser (Linux, macOS, or Windows). On Windows, substitute `.\mobile-use.exe` for `./mobile-use`.

```sh
./mobile-use up
```

Or run each step manually:

```sh
./mobile-use init
./mobile-use serve
./mobile-use ui
```

Keep the daemon running while using the console or an agent.

### Control an Android phone (USB or Wi-Fi ADB)

1. Install [Android SDK Platform Tools](https://developer.android.com/tools/releases/platform-tools) and add its directory to `PATH`.
2. Enable Developer options on the phone:
   - **USB:** enable USB debugging, connect the cable, and approve the computer’s key.
   - **Wi-Fi (same network):** enable **Wireless debugging**, then either open **Add device → Android Wi-Fi (ADB)** in the desktop console to scan and pair, or use the CLI:
     ```sh
     ./mobile-use wifi scan
     ./mobile-use wifi pair HOST:PAIRING_PORT 123456
     ./mobile-use wifi connect HOST:DEBUG_PORT
     ```
3. Run `./mobile-use devices` or `./mobile-use doctor`. In the console (or with `./mobile-use enroll <DEVICE_ID>`), enroll the phone. Use a personal profile for a phone whose sessions require owner approval. Use a dedicated profile for unattended, policy-granted devices.
4. Click **+ Allow local-agent (MCP)** in the console’s **Agent grants** editor (or pass `--grant local-agent=...` to `enroll`) to let your workstation agent inspect and tap.

For Unicode input and pinch, install `mobile-use-control-<version>.apk` with `adb install -r <apk>` and pass `--android-control-helper` to `serve`, `up`, or `connector`. For WebRTC viewing, download `scrcpy-server-v4.0` from the [official scrcpy release](https://github.com/Genymobile/scrcpy/releases/tag/v4.0) and pass `--scrcpy-server /path/to/scrcpy-server-v4.0`. The connector verifies the server file before capture.

### Pair the Android companion (QR code or Wi-Fi discovery)

Download `mobile-use-companion-<version>.apk` on Android 14 or later. Open it in the phone’s Files app and, if Android prompts you, allow that app to install the package. Official builds use the [Android release certificate](deploy/android-release-certificate.pem).

1. **Workstation over Wi-Fi (zero-config):** `mobile-use up` (or `serve`) automatically provisions a pinned TLS certificate in your config directory, binds a LAN companion listener, and advertises your computer via mDNS (`_mobile-use._tcp`). Click **+ Add device → Companion (QR & Wi-Fi)** in the console or run `./mobile-use pair` to display a QR code and 8-character pairing code (`XXXX-XXXX`).
2. **Pair from the phone:** Open the **mobile-use** app on Android and either:
   - Tap **Scan QR code** and scan the QR code shown in the console or terminal, or
   - Tap your computer under **Nearby on Wi-Fi**, verify the fingerprint, and enter the 8-character code.
3. **Remote HTTPS servers:** Configure `--companion-origin` on the remote server and point your local CLI with `./mobile-use remote set --url https://... --token <TOKEN>`. Then scan the QR code or choose **Remote HTTPS** in the Android app.
4. Keep the app available to review requests. Approve Android’s screen-sharing prompt and each camera or microphone request as needed. Use **Stop** in the app or console to end sharing, or **Unpair phone** / **Unpair companion** to revoke credentials.

The companion supports screen sharing, camera stills, and microphone clips without Android debugging. Touch, typing, and app control use the separate ADB enrollment.

### Connect an iPhone

Use a Mac with Xcode and a compatible Appium XCUITest driver. Provision signed WebDriverAgent, start Appium on loopback, and pass `--appium` and `--ios-config` to `serve`, `up`, or `connector`. The [iPhone setup guide](docs/operations.md#iphone-connector) covers inventory and video. Physical device certification is tracked in the [verification status](docs/verification.md).

## Connect an agent (Codex, Claude, Cursor)

Open the **MCP & Agents** tab in the desktop console or run `./mobile-use mcp-config` to copy exact setup commands for your system:

```sh
codex mcp add mobile-use -- /absolute/path/mobile-use mcp
claude mcp add --transport stdio mobile-use -- /absolute/path/mobile-use mcp
```

A workstation install reads its local agent token automatically. To connect the CLI and stdio MCP server to a remote server, run `./mobile-use remote set --url https://... --token <TOKEN>` (or set `MOBILE_USE_URL` and `MOBILE_USE_TOKEN`). Use the agent credential for automation and keep the admin credential for the console.

A network client can use Streamable HTTP at the private `https://<host>/mcp` endpoint. A typical MCP workflow calls `devices_list`, `session_start`, `session_status`, `observe`, `act`, and `session_end`. Use a fresh `frame_id` for screen-relative input and a unique `operation_id` for each action. If an outcome is unknown, observe the phone before retrying.

```mermaid
flowchart LR
    Request["Request access"] --> Profile{"Device profile"}
    Profile -->|"Personal"| Consent["Owner approves and stays present"]
    Profile -->|"Dedicated"| Grant["Policy grants the requested access"]
    Consent --> Lease["Active session"]
    Grant --> Lease
    Lease --> Control["Observe and act"]
    Control --> End["Stop or expire"]
```

## Install the private service

Download the deployment bundle and use `ghcr.io/upstandplatform/mobile-use:<version>` or the immutable reference in `oci-image.txt`. Authenticate to GHCR with a credential that has `read:packages` and access to the package. For offline installation, use `docker load -i <oci-linux-archive.tar.gz>` and set `MOBILE_USE_IMAGE` to the image name printed by Docker.

Copy the [environment template](deploy/mobile-use.env.example) to a private file outside the extracted bundle. Configure PostgreSQL, HTTPS certificates, an OIDC issuer and audience, and the connector CA using the [private deployment guide](docs/operations.md#private-service-and-connectors). Then run:

```sh
docker compose --env-file /path/to/mobile-use.env -f deploy/compose.yaml up -d --no-build
```

For Kubernetes, use the Helm release archive. Set `image`, `imagePullSecrets` for private registries, and the TLS and database Secrets described in the same guide.

## Build from source

Use Go 1.26+, Node.js 24+, and, for Android, JDK 17 and Android SDK 36.

```sh
npm --prefix web ci
npm --prefix web run build
go build -o mobile-use ./cmd/mobile-use
go test ./... -count=1
cd android
./gradlew :companion:assembleDebug :companion:lintDebug :control:assembleDebug
```

On Windows, build with `-o mobile-use.exe` and use `gradlew.bat`. See the [release process](docs/release.md) and the [verified platform matrix](docs/verification.md).
