# Deployment and device operations

## iPhone connector

On an Apple Silicon Mac, pair the phone through Xcode and complete the prompts for trust and Developer Mode. Provision a signed WebDriverAgent runner against the device and verify it in Xcode. Start a dedicated Appium instance with the XCUITest driver on loopback and leave relaxed security disabled. Enable Web Inspector on devices where Safari inspection is required. Appium and WebDriverAgent need compatible versions with the phone OS.

Use `--appium http://127.0.0.1:4723 --ios-config /absolute/path/to/ios-devices.json` with `serve` or `connector`. The inventory file has the following form.

```json
[
  {
    "udid": "IPHONE_DEVICE_IDENTIFIER",
    "label": "Development iPhone",
    "virtual": false,
    "wda_bundle_id": "org.example.WebDriverAgentRunner",
    "wda_port": 8101,
    "webkit_port": 27754
  }
]
```

A simulator entry uses its UUID and `virtual: true`. Start the simulator on the Mac. A prewarmed runner can be attached through a trusted loopback `wda_url` in the inventory. Assign unique ports when configuring multiple devices. Reserve a dedicated Mac user and Appium process for the fleet. The current inventory represents configured iOS devices while Appium is available. A device’s trust and runner availability are checked when a session connects.

Set `mjpeg_url` to the loopback WebDriverAgent screenshot stream to enable the iOS video adapter. Set `mjpeg_port` for a WDA process configured with a nondefault device-side port, and provision the matching loopback forward on the Mac. `--ios-ffmpeg` selects an FFmpeg build with `libx264`. The adapter checks the device lock state through Appium and converts MJPEG to H.264 at up to 8 fps. Physical-device validation is still open. [Appium MJPEG guide](https://appium.github.io/appium-xcuitest-driver/latest/guides/mjpeg/)

## Private service and connectors

Use `deploy/compose.yaml` with a PostgreSQL DSN, an HTTPS origin and certificate, a resource-scoped OIDC issuer, and the connector CA. Restrict the published ports to private interfaces or a VPN. Keep TLS private keys readable by the service user and keep the CA private key offline. Use your organization’s CA or `mobile-use pki init` and `mobile-use pki issue`. Issue `server` or `both` certificates with the relay hostname as a SAN, and `client` certificates with the assigned connector common name. List authorized connector identities in `MOBILE_USE_CONNECTORS` as `NAME=WORKSPACE` pairs. Rotate and reissue in advance of expiry. Removing a name from the configuration and restarting affected relay replicas revokes that connector’s admission.

Copy `deploy/mobile-use.env.example` to a private file. Generate independent passwords and the hexadecimal digest key with `openssl rand -hex 32`. Put `http.crt`, `http.key`, `relay.crt`, `relay.key`, and `relay-ca.crt` in the host directory named by `MOBILE_USE_TLS_DIRECTORY`. Set the bind address to a reachable private interface when the browser or connectors run on a different host. Use the origin and ports that the clients will actually reach.

With Compose, set `POSTGRES_BOOTSTRAP_PASSWORD` for the database administrator and `POSTGRES_PASSWORD` for the `mobileuse` runtime role. Point `MOBILE_USE_POSTGRES` at the runtime role using `postgres` as the hostname. The official PostgreSQL container runs the initialization script on first boot of a fresh volume. For an existing volume or external PostgreSQL, have the database administrator provision a login with `NOSUPERUSER` and `NOBYPASSRLS`, grant ownership of the application schema and any existing mobile-use tables to that login, and use it in the DSN. Back up the database before transferring ownership. The service enables and forces row-level security on startup. Use a TLS-verified DSN for external database connections.

Configure the OIDC issuer to issue short-lived signed access tokens with:

- `aud` matching `MOBILE_USE_OIDC_AUDIENCE`, normally the HTTPS `/mcp` resource
- scope `mobile-use` and access-token `typ` of `Bearer`, `at+jwt`, or `access`
- `mobile_use_workspace` and a single `mobile_use_role` set to `admin`, `operator`, `owner`, `agent`, or `auditor`
- a stable `sub`, used in owner assignments and agent grants

Pre-register the console client as a public client with authorization-code flow, PKCE S256, the exact `<origin>/auth/callback` redirect URI, and the console web origin for CORS. Keep client metadata and agent-service credentials under operator control. Set OAuth resource audience and role mapping from administrator-controlled attributes. Never use editable user profile fields for role assignment.

`deploy/keycloak/realm.example.json` provides a starting realm with the console client and access-token mappers. Substitute the production origin and audience before importing. `deploy/helm/mobile-use` runs the control plane as a StatefulSet against external PostgreSQL. Supply the database, HTTP TLS, and relay TLS Secrets named in its values, and issue the relay certificate for both server and client use. Include the peer DNS names in its SANs, in addition to the connector-facing relay hostname. The chart defaults to two replicas with companion support disabled because the current companion frame and sensor buffers are process-local.

On a connector host, run:

```sh
mobile-use connector --endpoint relay.example.internal:9443 \
  --workspace team --name android-host-1 \
  --cert android-host-1.crt --key android-host-1.key --ca relay-ca.crt
```

The private HTTP endpoint exposes `/healthz`, `/readyz`, and an authorized Prometheus-format `/v1/metrics`. Back up both PostgreSQL and the operation digest configuration. Restore to an isolated environment before connecting production agents or phones and invalidate active session leases during a restore drill. The connector relay can forward to an owning replica over mTLS when each replica is configured with a distinct `--relay-address` and allowlisted `--relay-peers`. Companion media buffers are process-local, so route companion use to one instance until a shared data-plane design is deployed.

## Live video across private networks

Android connectors verify the SHA-256 of the official scrcpy 4.0 server before capture. When the connector and console service run separately, the H.264 packets cross a dedicated stream on their mTLS link. The browser receives video through a WebRTC peer on the console service.

Direct ICE connections work when the browser can reach the API host's ICE network addresses. For networks that need a relay, `deploy/turn.compose.yaml` configures coturn on a Linux host. Set the listener interface, advertised IP or NAT mapping, certificate directory, realm, and shared secret. Allow the configured TURN listener and UDP relay port range for authorized networks. Use the same shared secret with `MOBILE_USE_TURN_URLS` on the API. For example, use `turns:turn.example.internal:5349?transport=tcp`. `MOBILE_USE_TURN_RELAY_ONLY=1` requires TURN for both WebRTC peers. The Helm chart exposes the corresponding `turnURLs`, `turnSecret`, and `turnRelayOnly` values.

## Android app maintenance

Pass `--android-catalog /absolute/path/catalog.json` and `--aapt2 /path/to/Android/build-tools/version/aapt2` to the connector or workstation daemon. Each catalog entry has `id`, `label`, `app_id`, `path`, and `sha256`. `path` points to a signed APK on the connector. Relative paths are resolved against the catalog directory. Use the SHA-256 of the distributed file. The connector checks the staged bytes and the APK's manifest package before sending it to the Android installer. Android enforces its own signature and installation rules.

An operator or administrator must hold a dedicated session with Apps and Touch access to use App maintenance in the console. Install updates the approved package on the device. Reset clears the selected app's data. The confirmation is the operation verb and catalog ID, such as `reset demo-app`. The catalog does not expose file paths or arbitrary shell commands to callers.

Maintenance operations have a 20-second command deadline. Test representative APKs on the connector hardware before adding them to the catalog. If installation times out, check the installed package and its version before retrying.
