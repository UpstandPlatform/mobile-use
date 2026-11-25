import { useCallback, useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import { api } from './api';
import type { Actor, CompanionEndpoint, PairingSession, PairingStatus, SystemInfo, WirelessAdbDiscovery } from './api';
import { QrCode } from './qr';

type AddTab = 'companion' | 'wifi-adb' | 'remote';

function shortFingerprint(hex?: string): string {
  if (!hex) return '';
  const clean = hex.trim().toUpperCase();
  if (clean.length < 16) return clean;
  return clean.slice(0, 16).match(/.{1,2}/g)?.join(':') || clean;
}

export function CopyButton({ value, label = 'Copy' }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <button
      type="button"
      className="copy-btn"
      onClick={() => {
        navigator.clipboard?.writeText(value).then(() => {
          setCopied(true);
          window.setTimeout(() => setCopied(false), 1600);
        }).catch(() => undefined);
      }}
    >
      {copied ? 'Copied' : label}
    </button>
  );
}

export function AddDevicePanel({
  token,
  actor,
  systemInfo,
  companionEnabled,
  onClose,
  onDeviceAdded,
}: {
  token: string;
  actor: Actor;
  systemInfo?: SystemInfo;
  companionEnabled: boolean;
  onClose: () => void;
  onDeviceAdded: (deviceId?: string) => Promise<void>;
}) {
  const canPairCompanion = companionEnabled && (['admin', 'operator', 'owner'].includes(actor.role));
  const supportsWifiAdb = Boolean(systemInfo?.wireless_adb_supported && ['admin', 'operator'].includes(actor.role));
  const defaultTab: AddTab = canPairCompanion ? 'companion' : supportsWifiAdb ? 'wifi-adb' : 'remote';
  const [tab, setTab] = useState<AddTab>(defaultTab);

  // Companion QR & code state
  const [pairing, setPairing] = useState<PairingSession>();
  const [pairStatus, setPairStatus] = useState<PairingStatus>();
  const [selectedOrigin, setSelectedOrigin] = useState('');
  const [autoEnroll, setAutoEnroll] = useState(systemInfo?.mode === 'workstation' || actor.workspace === 'local');
  const [pairLoading, setPairLoading] = useState(false);
  const [pairError, setPairError] = useState('');
  const [nowMs, setNowMs] = useState(() => Date.now());

  // Wi-Fi ADB state
  const [adbDiscovery, setAdbDiscovery] = useState<WirelessAdbDiscovery>();
  const [adbScanning, setAdbScanning] = useState(false);
  const [pairAddr, setPairAddr] = useState('');
  const [pairCode, setPairCode] = useState('');
  const [connectAddr, setConnectAddr] = useState('');
  const [adbBusy, setAdbBusy] = useState(false);
  const [adbNotice, setAdbNotice] = useState('');
  const [adbError, setAdbError] = useState('');

  const createPairing = useCallback(async (originOverride?: string, enrollFlag?: boolean) => {
    setPairLoading(true);
    setPairError('');
    setPairStatus(undefined);
    try {
      const body: Record<string, unknown> = { auto_enroll: enrollFlag ?? autoEnroll };
      const targetOrigin = originOverride !== undefined ? originOverride : selectedOrigin;
      if (targetOrigin) body.origin = targetOrigin;
      const created = await api<PairingSession>(token, '/v1/companion/pairing', 'POST', body);
      setPairing(created);
      if (!targetOrigin && created.origin) setSelectedOrigin(created.origin);
    } catch (err) {
      setPairError(err instanceof Error ? err.message : 'Could not create pairing code.');
    } finally {
      setPairLoading(false);
    }
  }, [token, autoEnroll, selectedOrigin]);

  useEffect(() => {
    if (tab === 'companion' && canPairCompanion && !pairing && !pairLoading && !pairError) {
      void createPairing();
    }
  }, [tab, canPairCompanion, pairing, pairLoading, pairError, createPairing]);

  useEffect(() => {
    if (!pairing || pairStatus?.state === 'paired' || pairStatus?.state === 'expired') return;
    const timer = window.setInterval(async () => {
      setNowMs(Date.now());
      try {
        const next = await api<PairingStatus>(token, `/v1/companion/pairing/${pairing.id}`);
        setPairStatus(next);
        if (next.state === 'paired') {
          await onDeviceAdded(next.device_id);
        }
      } catch {
        // Ignore transient poll errors while waiting
      }
    }, 1500);
    return () => window.clearInterval(timer);
  }, [pairing, pairStatus?.state, token, onDeviceAdded]);

  useEffect(() => {
    const t = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(t);
  }, []);

  const scanWifiAdb = useCallback(async () => {
    setAdbScanning(true);
    setAdbError('');
    try {
      const info = await api<WirelessAdbDiscovery>(token, '/v1/local/adb/discover');
      setAdbDiscovery(info);
    } catch (err) {
      setAdbError(err instanceof Error ? err.message : 'Could not scan for Wi-Fi ADB devices.');
    } finally {
      setAdbScanning(false);
    }
  }, [token]);

  useEffect(() => {
    if (tab === 'wifi-adb' && supportsWifiAdb && !adbDiscovery && !adbScanning) {
      void scanWifiAdb();
    }
  }, [tab, supportsWifiAdb, adbDiscovery, adbScanning, scanWifiAdb]);

  async function submitWifiPair(event: FormEvent) {
    event.preventDefault();
    setAdbBusy(true);
    setAdbNotice('');
    setAdbError('');
    try {
      const res = await api<{ message: string }>(token, '/v1/local/adb/pair', 'POST', { address: pairAddr.trim(), code: pairCode.trim() });
      setAdbNotice(res.message || 'Paired successfully. Now connect using the Wireless Debugging IP and port.');
      setPairCode('');
      await scanWifiAdb();
    } catch (err) {
      setAdbError(err instanceof Error ? err.message : 'Wi-Fi pairing failed.');
    } finally {
      setAdbBusy(false);
    }
  }

  async function submitWifiConnect(address: string) {
    setAdbBusy(true);
    setAdbNotice('');
    setAdbError('');
    try {
      const res = await api<{ message: string }>(token, '/v1/local/adb/connect', 'POST', { address: address.trim() });
      setAdbNotice(res.message || `Connected to ${address}.`);
      await onDeviceAdded();
    } catch (err) {
      setAdbError(err instanceof Error ? err.message : 'Could not connect over Wi-Fi.');
    } finally {
      setAdbBusy(false);
    }
  }

  const endpoints: CompanionEndpoint[] = pairing?.origins || systemInfo?.companion_origins || [];
  const expiresSec = pairing ? Math.max(0, Math.floor((new Date(pairing.expires_at).getTime() - nowMs) / 1000)) : 0;
  const countdown = `${Math.floor(expiresSec / 60)}:${String(expiresSec % 60).padStart(2, '0')}`;
  const fpShort = shortFingerprint(pairing?.fingerprint || systemInfo?.companion_fingerprint);

  return (
    <section className="surface add-device" aria-label="Add a phone or device">
      <div className="section-heading">
        <div>
          <h2>Add device</h2>
          <span>Pair over local Wi-Fi or connect a remote device</span>
        </div>
        <button type="button" onClick={onClose}>Close</button>
      </div>

      <div className="subtabs" role="tablist" aria-label="Connection methods">
        {canPairCompanion && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'companion'}
            className={tab === 'companion' ? 'active' : ''}
            onClick={() => setTab('companion')}
          >
            Companion (QR &amp; Wi-Fi)
          </button>
        )}
        {supportsWifiAdb && (
          <button
            type="button"
            role="tab"
            aria-selected={tab === 'wifi-adb'}
            className={tab === 'wifi-adb' ? 'active' : ''}
            onClick={() => setTab('wifi-adb')}
          >
            Android Wi-Fi (ADB)
          </button>
        )}
        <button
          type="button"
          role="tab"
          aria-selected={tab === 'remote'}
          className={tab === 'remote' ? 'active' : ''}
          onClick={() => setTab('remote')}
        >
          Remote &amp; iPhone
        </button>
      </div>

      {tab === 'companion' && canPairCompanion && (
        <div className="add-body">
          {pairError && <div className="notice" role="alert"><span>{pairError}</span></div>}
          {pairStatus?.state === 'paired' ? (
            <div className="pair-success" role="status">
              <h3>Paired: {pairStatus.label || 'Android device'}</h3>
              <p>
                {pairStatus.auto_enrolled
                  ? 'The phone is enrolled and ready for MCP agent sessions.'
                  : 'The phone is connected. Review its enrollment and agent access in the device panel.'}
              </p>
              <div className="actions">
                <button type="button" className="primary" onClick={() => void createPairing()}>Pair another phone</button>
                <button type="button" onClick={onClose}>Done</button>
              </div>
            </div>
          ) : (
            <div className="pair-grid">
              <div className="qr-card">
                {pairing?.qr_uri ? (
                  <QrCode value={pairing.qr_uri} label="QR code for pairing Android companion" size={212} />
                ) : (
                  <div className="qr-placeholder">{pairLoading ? 'Generating QR…' : 'QR unavailable'}</div>
                )}
                <div className="pair-code-box">
                  <span className="hint">Pairing code</span>
                  <div className="code-row">
                    <code className="big-code">{pairing?.code || '---- ----'}</code>
                    {pairing?.code && <CopyButton value={pairing.code} />}
                  </div>
                  <span className="hint">
                    {pairStatus?.state === 'expired' || (pairing && expiresSec === 0)
                      ? 'Code expired — generate a new code'
                      : pairing
                        ? `Waiting for phone · expires in ${countdown}`
                        : 'Preparing pairing session…'}
                  </span>
                </div>
              </div>

              <div className="pair-details">
                <div className="pair-steps">
                  <h3>Connect with the mobile-use Android app</h3>
                  <ol>
                    <li>Keep this computer and your phone on the same Wi-Fi network (or use your remote HTTPS endpoint).</li>
                    <li>Open <strong>mobile-use</strong> on Android and tap <strong>Scan QR code</strong>, or tap this computer under <strong>Nearby on Wi-Fi</strong> and enter the 8-character code.</li>
                  </ol>
                </div>

                {endpoints.length > 1 && (
                  <label>
                    Network interface
                    <select
                      value={selectedOrigin || pairing?.origin || ''}
                      disabled={pairLoading}
                      onChange={event => {
                        const next = event.target.value;
                        setSelectedOrigin(next);
                        void createPairing(next, autoEnroll);
                      }}
                    >
                      {endpoints.map(ep => (
                        <option key={ep.origin} value={ep.origin}>
                          {ep.origin}{ep.interface ? ` (${ep.interface})` : ''}{ep.primary ? ' · Primary' : ''}
                        </option>
                      ))}
                    </select>
                  </label>
                )}

                <dl className="meta-list">
                  <dt>Server address</dt>
                  <dd>
                    <code>{pairing?.origin || systemInfo?.companion_origin || '—'}</code>
                    {pairing?.origin && <CopyButton value={pairing.origin} />}
                  </dd>
                  <dt>Mode</dt>
                  <dd>{(pairing?.mode || systemInfo?.companion_mode) === 'lan' ? 'Local Wi-Fi (Pinned TLS + mDNS)' : 'Remote HTTPS'}</dd>
                  {fpShort && (
                    <>
                      <dt>Fingerprint</dt>
                      <dd><code>{fpShort}</code></dd>
                    </>
                  )}
                </dl>

                {['admin', 'operator'].includes(actor.role) && (
                  <label className="checkbox-inline">
                    <input
                      type="checkbox"
                      checked={autoEnroll}
                      disabled={pairLoading}
                      onChange={event => {
                        const next = event.target.checked;
                        setAutoEnroll(next);
                        void createPairing(selectedOrigin || undefined, next);
                      }}
                    />
                    <span>Automatically enroll for local MCP agent (<code>local-agent</code>)</span>
                  </label>
                )}

                <div className="actions">
                  <button type="button" className="primary" disabled={pairLoading} onClick={() => void createPairing()}>
                    {pairLoading ? 'Generating…' : 'New pairing code'}
                  </button>
                </div>
              </div>
            </div>
          )}
        </div>
      )}

      {tab === 'wifi-adb' && supportsWifiAdb && (
        <div className="add-body">
          {adbError && <div className="notice" role="alert"><span>{adbError}</span></div>}
          {adbNotice && <div className="notice notice-ok" role="status"><span>{adbNotice}</span></div>}
          {adbDiscovery && !adbDiscovery.adb_available && (
            <div className="notice" role="alert">
              <span>Android platform-tools (<code>adb</code>) was not found in <code>PATH</code>. Install Android SDK Platform Tools to use USB or Wi-Fi ADB.</span>
            </div>
          )}

          <div className="wifi-header">
            <p className="hint">
              Enable <strong>Developer options → Wireless debugging</strong> on your Android phone. USB-connected phones appear in Inventory automatically.
            </p>
            <button type="button" disabled={adbScanning} onClick={() => void scanWifiAdb()}>
              {adbScanning ? 'Scanning…' : 'Scan Wi-Fi'}
            </button>
          </div>

          {adbDiscovery && adbDiscovery.wireless_services.length > 0 && (
            <div className="discovered-list">
              <h3>Discovered on Wi-Fi</h3>
              <ul>
                {adbDiscovery.wireless_services.map(svc => (
                  <li key={`${svc.type}-${svc.address}`}>
                    <div>
                      <strong>{svc.type === 'pairing' ? 'Pairing endpoint' : 'Debug endpoint'}</strong>
                      <span><code>{svc.address}</code> · {svc.name}</span>
                    </div>
                    {svc.type === 'pairing' ? (
                      <button type="button" onClick={() => setPairAddr(svc.address)}>Use for pairing</button>
                    ) : (
                      <button type="button" className="primary" disabled={adbBusy} onClick={() => void submitWifiConnect(svc.address)}>Connect</button>
                    )}
                  </li>
                ))}
              </ul>
            </div>
          )}

          <div className="wifi-forms">
            <form className="wifi-card" onSubmit={submitWifiPair}>
              <h3>1. Pair new phone (first time)</h3>
              <p className="hint">On the phone, tap <strong>Pair device with pairing code</strong>.</p>
              <label>
                IP &amp; pairing port
                <input required placeholder="192.168.1.42:37199" value={pairAddr} onChange={e => setPairAddr(e.target.value)} />
              </label>
              <label>
                6-digit pairing code
                <input required pattern="[0-9]{6}" maxLength={6} placeholder="123456" value={pairCode} onChange={e => setPairCode(e.target.value)} />
              </label>
              <button type="submit" disabled={adbBusy || !pairAddr.trim() || pairCode.trim().length !== 6}>Pair over Wi-Fi</button>
            </form>

            <form className="wifi-card" onSubmit={e => { e.preventDefault(); void submitWifiConnect(connectAddr); }}>
              <h3>2. Connect paired phone</h3>
              <p className="hint">Enter the IP &amp; port shown on the main Wireless debugging screen.</p>
              <label>
                IP &amp; debug port
                <input required placeholder="192.168.1.42:41235" value={connectAddr} onChange={e => setConnectAddr(e.target.value)} />
              </label>
              <button type="submit" className="primary" disabled={adbBusy || !connectAddr.trim()}>Connect device</button>
            </form>
          </div>
        </div>
      )}

      {tab === 'remote' && (
        <div className="add-body remote-guide">
          <div className="guide-card">
            <h3>Connect a remote Android/iPhone host (mTLS Connector)</h3>
            <p className="hint">Run the connector on any Linux, macOS, or Windows machine with USB/Wi-Fi devices and point it at your private relay:</p>
            <pre><code>{`mobile-use connector --endpoint relay.example.internal:9443 \\\n  --workspace ${actor.workspace} --name android-host-1 \\\n  --cert android-host-1.crt --key android-host-1.key --ca relay-ca.crt`}</code></pre>
          </div>
          <div className="guide-card">
            <h3>Use the CLI against a remote server</h3>
            <p className="hint">Point your local CLI and MCP tools to a remote mobile-use deployment:</p>
            <pre><code>{`mobile-use remote set --url https://mobile-use.example.com --token <TOKEN>\nmobile-use pair`}</code></pre>
          </div>
          <div className="guide-card">
            <h3>Connect an iPhone (macOS)</h3>
            <p className="hint">On a Mac with Xcode, WebDriverAgent, and Appium running on loopback, start the daemon or connector with <code>--appium http://127.0.0.1:4723 --ios-config ios-devices.json</code>.</p>
          </div>
        </div>
      )}
    </section>
  );
}
