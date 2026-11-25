# mobile-use

mobile-use gives agents session-based access to enrolled Android phones and iPhones. It includes an operator console, an HTTP API, MCP over stdio and Streamable HTTP, a workstation daemon, a remote connector, and an Android companion.

## Workstation setup

Install Go 1.26+, Node.js 24+, and the Android SDK Platform Tools. For iPhone access, install Xcode and a compatible Appium XCUITest driver on a Mac and provision WebDriverAgent on the iPhone.

```sh
npm --prefix web ci
npm --prefix web run build
go build -o mobile-use ./cmd/mobile-use
./mobile-use init
./mobile-use serve
```

On Windows, build with `go build -o mobile-use.exe ./cmd/mobile-use` and run `./mobile-use.exe`. Open the loopback console, and use the credential shown by `mobile-use token admin`. Set an owner and grant the capabilities the agent needs to `local-agent`. Personal sessions require approval and owner presence. Give unattended access only to dedicated devices. Use `adb devices -l` or `mobile-use doctor` to verify that Android authorization succeeded. `adb pair` and `adb connect` handle Android Wireless Debugging setup on the connector host.

For Unicode entry into editable Android fields and multi-touch gestures, build `:control:assembleDebug` in `android`, install the trusted control helper with ADB, and pass `--android-control-helper` to `serve` or `connector`. The control helper requires authorized Android debugging. The companion has its own build and permissions.

Pass the verified `scrcpy-server-v4.0` file as `--scrcpy-server` to enable H.264 live viewing on Android. The console's Live video button opens a WebRTC viewer after session approval. Agents and the snapshot controls use the same authorized session. Download the server file from the official release. [scrcpy 4.0 release](https://github.com/Genymobile/scrcpy/releases/tag/v4.0)

## Agents

Register the absolute `mobile-use` binary path and the argument `mcp` as a stdio MCP server. The CLI loads the workstation agent credential automatically. For another server, set `MOBILE_USE_URL` to its origin and provide the agent access token in `MOBILE_USE_TOKEN`. A network-reachable agent can instead use Streamable HTTP at `/mcp` with the self-hosted identity provider.

The normal flow is `devices_list`, `session_start`, `session_status`, `observe`, `act`, and `session_end`. Use the most recent `frame_id` for native input. Provide a unique `operation_id` for each input action. If an outcome is unknown, observe the screen before deciding what to do next. The API accepts coordinates as fractions in `[0, 1)`.

## Android companion

Build `:companion:assembleDebug` with the Gradle wrapper in `android`. Install the APK on an Android 14+ device. The companion connects to an HTTPS origin available on the phone. Android must trust the certificate and verify the hostname. For local workstation use, enable a separate companion listener with `--companion-listen`, `--companion-origin`, `--companion-cert`, and `--companion-key`. The console creates a short-lived pairing code. After pairing, enroll the companion in the console with the owner who created the code.

A companion session supports screen sharing through Android’s consent prompt, camera stills through the system camera, and approved microphone clips. Device control uses the separate ADB enrollment. The companion has an in-app Stop control and a screen-sharing notification. Unpair in the app and revoke its server credential from the API with `DELETE /v1/companion/{id}` when decommissioning a phone.

## iPhone and private hosting

See the [operations guide](docs/operations.md) for readying Appium, issuing connector certificates, deploying PostgreSQL and TLS, and running smoke tests. See the [current verification scope](docs/verification.md) for tested behavior and production work that remains.
