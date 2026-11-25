import { useCallback, useEffect, useRef, useState } from 'react';
import type { FormEvent, ReactNode, PointerEvent } from 'react';
import { createRoot } from 'react-dom/client';
import { api, beginLogin, completeLogin, labels } from './api';
import type { Actor, Audit, Device, Observation, OIDC, Session } from './api';
import { LiveVideo } from './LiveVideo';
import './styles.css';

type Tab = 'Devices' | 'Sessions' | 'Audit';
const time = (date: string) => new Date(date).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
const errorText = (error: unknown) => error instanceof Error ? error.message : 'Something went wrong.';

function PhoneMark({ small = false }: { small?: boolean }) {
  return <span className={small ? 'phone-mark phone-mark-small' : 'phone-mark'} aria-hidden="true"><i /><b /></span>;
}

function Badge({ children, tone = 'neutral' }: { children: ReactNode; tone?: 'neutral' | 'green' | 'amber' }) {
  return <span className={`badge badge-${tone}`}><i />{children}</span>;
}

function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem('mobile-use.token') || '');
  const [draftToken, setDraftToken] = useState('');
  const [oidc, setOIDC] = useState<OIDC>();
  const [actor, setActor] = useState<Actor>();
  const [devices, setDevices] = useState<Device[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [audits, setAudits] = useState<Audit[]>([]);
  const [tab, setTab] = useState<Tab>('Devices');
  const [selected, setSelected] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [captureRevision, setCaptureRevision] = useState(0);
  const [pairing, setPairing] = useState<{ code: string; origin: string; expires_at: string }>();

  const signedIn = useCallback((value: string) => {
    sessionStorage.setItem('mobile-use.token', value);
    setToken(value);
    setDraftToken('');
  }, []);

  useEffect(() => {
    fetch('/auth/config').then(response => response.json()).then(async (value: OIDC) => {
      setOIDC(value);
      if (location.pathname === '/auth/callback') signedIn(await completeLogin(value));
    }).catch(error => setError(errorText(error)));
  }, [signedIn]);

  const refresh = useCallback(async (credential: string, identity: Actor) => {
    const nextDevices = await api<Device[]>(credential, '/v1/devices');
    const nextSessions = await api<Session[]>(credential, '/v1/sessions');
    setDevices(nextDevices);
    setSessions(nextSessions);
    setSelected(current => nextDevices.some(device => device.id === current) ? current : nextDevices[0]?.id || '');
    if (tab === 'Audit' && ['admin', 'auditor'].includes(identity.role)) {
      setAudits(await api<Audit[]>(credential, '/v1/audit'));
    }
  }, [tab]);

  useEffect(() => {
    if (!token) return;
    let ignore = false;
    api<Actor>(token, '/v1/me').then(async identity => {
      if (ignore) return;
      setActor(identity);
      await refresh(token, identity);
    }).catch(error => {
      if (!ignore) {
        setActor(undefined);
        setError(errorText(error));
      }
    });
    return () => { ignore = true; };
  }, [token, refresh]);

  useEffect(() => {
    if (!token || !actor) return;
    const timer = window.setInterval(() => {
      if (!document.hidden) refresh(token, actor).catch(error => setError(errorText(error)));
    }, 3500);
    return () => window.clearInterval(timer);
  }, [actor, token, refresh]);

  async function run(task: () => Promise<unknown>) {
    setBusy(true);
    setError('');
    try {
      await task();
      if (actor) await refresh(token, actor);
    } catch (error) {
      setError(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  function signOut() {
    sessionStorage.removeItem('mobile-use.token');
    setToken('');
    setActor(undefined);
    setDevices([]);
    setSessions([]);
    setAudits([]);
    setPairing(undefined);
    setError('');
  }

  if (!actor) return <div className="sign-in">
    <div className="sign-in-card">
      <PhoneMark />
      <h1>mobile-use</h1>
      <p>Connect and control your enrolled phones.</p>
      {error && <div className="notice" role="alert">{error}</div>}
      {oidc?.client_id ? <button type="button" className="primary" onClick={() => beginLogin(oidc).catch(error => setError(errorText(error)))}>Sign in</button> :
        <form onSubmit={event => { event.preventDefault(); setError(''); signedIn(draftToken.trim()); }}>
          <label htmlFor="credential">Access token</label>
          <input id="credential" type="password" autoComplete="off" required value={draftToken} onChange={event => setDraftToken(event.target.value)} />
          <button className="primary" type="submit">Open console</button>
          <p className="hint">For a local installation, run <code>mobile-use token admin</code>.</p>
        </form>}
    </div>
  </div>;

  const manage = ['admin', 'operator'].includes(actor.role);
  const device = devices.find(item => item.id === selected);
  const deviceSessions = sessions.filter(item => item.device_id === selected);
  const active = deviceSessions.find(item => item.state === 'active');
  const tabs: Tab[] = ['Devices', 'Sessions'];
  if (['admin', 'auditor'].includes(actor.role)) tabs.push('Audit');
  const change = (session: Session, verb: string) => run(() => api(token, `/v1/sessions/${session.id}/${verb}`, 'POST'));

  return <div className="layout">
    <header className="top">
      <a className="brand" href="/"> <PhoneMark small /> <span>mobile-use</span></a>
      <nav aria-label="Workspace">{tabs.map(item => <button key={item} type="button" aria-current={item === tab ? 'page' : undefined} onClick={() => setTab(item)}>{item}</button>)}</nav>
      <div className="identity"><span>{actor.workspace}</span><button type="button" onClick={signOut}>Sign out</button></div>
    </header>
    <main className="workspace">
      <div className="title-row"><h1>{tab}</h1>
        <div className="actions">{oidc?.companion_enabled && tab === 'Devices' && (manage || actor.role === 'owner') && <button type="button" className="primary" onClick={() => run(async () => setPairing(await api(token, '/v1/companion/pairing', 'POST')))} disabled={busy}>Pair Android</button>}
          <button type="button" onClick={() => run(() => refresh(token, actor))} disabled={busy}>Refresh</button></div>
      </div>
      {error && <div className="notice" role="alert"><span>{error}</span><button type="button" onClick={() => setError('')}>Dismiss</button></div>}
      {pairing && tab === 'Devices' && <section className="surface pairing" aria-label="Pair an Android companion">
        <div><h2>Pair an Android companion</h2><p>Open mobile-use on the phone and enter the server address and code before {time(pairing.expires_at)}.</p>
          <dl><dt>Server address</dt><dd><code>{pairing.origin}</code></dd><dt>Pairing code</dt><dd><code>{pairing.code}</code></dd></dl></div>
        <button type="button" onClick={() => setPairing(undefined)}>Close</button>
      </section>}
      {tab === 'Devices' && <div className="device-layout">
        {devices.length > 1 && <label className="device-picker">Device<select value={selected} onChange={event => setSelected(event.target.value)}>
          {devices.map(item => <option key={item.id} value={item.id}>{item.label} · {item.online ? 'Online' : 'Offline'}</option>)}
        </select></label>}
        <section className={`surface inventory${devices.length ? ' inventory-populated' : ''}`} aria-label="Device inventory">
          <div className="section-heading"><h2>Inventory</h2><span>{devices.length} {devices.length === 1 ? 'device' : 'devices'}</span></div>
          {devices.length === 0 ? <div className="empty"><PhoneMark /><h3>No devices available</h3><p>Connect an authorized phone or start an emulator on an enrolled connector.</p></div> : <ul className="device-list">{devices.map(item => <li key={item.id}>
            <button type="button" className={item.id === selected ? 'selected' : ''} onClick={() => setSelected(item.id)} aria-pressed={item.id === selected}>
              <PhoneMark small /><span><strong>{item.label}</strong><small>{item.platform === 'ios' ? 'iOS' : 'Android'} · {item.virtual ? 'Virtual device' : item.profile || 'Awaiting enrollment'}</small></span>
              <Badge tone={item.online ? 'green' : 'neutral'}>{item.online ? 'Online' : 'Offline'}</Badge>
            </button>
          </li>)}</ul>}
        </section>
        {device && <section className="surface detail" aria-label="Selected device">
          <div className="section-heading"><div><h2>{device.label}</h2><span>{device.platform === 'ios' ? 'iOS' : 'Android'} · {device.connector || device.mode}</span></div><Badge tone={device.online ? 'green' : 'neutral'}>{device.online ? 'Online' : 'Offline'}</Badge></div>
          <div className="detail-body">
            {deviceSessions.length > 0 && <SessionList sessions={deviceSessions} devices={devices} actor={actor} busy={busy} change={change} selectedDevice={device.id} />}
            {active && <Viewer key={`${active.id}:${active.controller}`} token={token} actor={actor} session={active} supportsVideo={device.live_video} captureRevision={captureRevision} onError={setError} />}
            {manage && active?.controller === actor.subject && device.profile === 'dedicated' && !!device.catalog?.length && active.capabilities.includes('apps') && active.capabilities.includes('touch') && !active.paused &&
              <MaintenanceForm key={active.id} device={device} token={token} session={active} onAttempt={() => setCaptureRevision(value => value + 1)} />}
            {device.enabled && device.online && !active && actor.role !== 'auditor' && <StartForm key={`start-${device.id}`} device={device} busy={busy} submit={body => run(() => api(token, '/v1/sessions', 'POST', body))} />}
            {manage && <EnrollmentForm key={device.id} actor={actor} device={device} busy={busy} submit={body => run(() => api(token, `/v1/devices/${device.id}`, 'PUT', body))} />}
            <details className="enrollment capability-list"><summary>Device capabilities</summary><p>{Object.keys(device.capabilities).filter(cap => device.capabilities[cap]).map(cap => labels[cap] || cap).join(' · ') || 'None available'}</p></details>
          </div>
        </section>}
      </div>}
      {tab === 'Sessions' && <section className="surface detail"><SessionList sessions={sessions} devices={devices} actor={actor} busy={busy} change={change} /><div className="detail-body">{sessions.filter(item => item.state === 'active').map(item => <Viewer key={item.id} token={token} actor={actor} session={item} supportsVideo={devices.some(device => device.id === item.device_id && device.live_video)} onError={setError} />)}</div></section>}
      {tab === 'Audit' && <section className="surface audit"><table><caption className="sr-only">Workspace audit events</caption><thead><tr><th>Time</th><th>Event</th><th>Identity</th><th>Result</th></tr></thead><tbody>{audits.map((item, index) => <tr key={`${item.at}-${index}`}><td>{new Date(item.at).toLocaleString()}</td><td>{item.event.replaceAll('_', ' ')}</td><td>{item.actor}</td><td>{item.code?.replaceAll('_', ' ') || '—'}</td></tr>)}</tbody></table>{audits.length === 0 && <p className="empty">No audit events yet.</p>}</section>}
    </main>
  </div>;
}

function SessionList({ sessions, devices, actor, busy, change, selectedDevice }: { sessions: Session[]; devices: Device[]; actor: Actor; busy: boolean; change: (item: Session, verb: string) => void; selectedDevice?: string }) {
  if (sessions.length === 0) return <p className="empty">No active sessions or requests.</p>;
  return <ul className="sessions">{sessions.map(item => <li key={item.id}>
    <div><Badge tone={item.state === 'active' && !item.paused ? 'green' : 'amber'}>{item.paused ? 'Paused' : item.state === 'active' ? 'Active' : 'Approval requested'}</Badge><h3>{item.purpose}</h3>
      <p>{selectedDevice ? '' : `${devices.find(device => device.id === item.device_id)?.label || 'Phone'} · `}{item.requester} · expires {time(item.expires_at)}</p>
      <small>{item.capabilities.map(cap => labels[cap] || cap).join(', ')}</small>
    </div>
    {item.owner === actor.subject && item.state === 'pending_approval' && <div className="actions"><button type="button" disabled={busy} onClick={() => change(item, 'deny')}>Deny</button><button className="primary" type="button" disabled={busy} onClick={() => change(item, 'approve')}>Approve</button></div>}
    {item.state === 'active' && (item.owner === actor.subject || item.requester === actor.subject || ['admin', 'operator'].includes(actor.role)) && <div className="actions">
      {item.owner === actor.subject && <button type="button" disabled={busy} onClick={() => change(item, item.paused ? 'resume' : 'pause')}>{item.paused ? 'Resume' : 'Pause'}</button>}
      {item.owner === actor.subject && item.controller !== actor.subject && <button type="button" disabled={busy} onClick={() => change(item, 'takeover')}>Take over</button>}
      <button type="button" className="danger" disabled={busy} onClick={() => change(item, 'end')}>Stop</button>
    </div>}
  </li>)}</ul>;
}

function EnrollmentForm({ actor, device, busy, submit }: { actor: Actor; device: Device; busy: boolean; submit: (data: unknown) => void }) {
  const [name, setName] = useState(device.label);
  const [profile, setProfile] = useState(device.profile || 'personal');
  const [owner, setOwner] = useState(device.owner || actor.subject);
  const [grants, setGrants] = useState(device.grants?.map(grant => `${grant.subject}=${grant.capabilities.join(',')}`).join('\n') || '');
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const requested = Object.keys(device.capabilities).filter(cap => device.capabilities[cap]);
  function onboard(event: FormEvent) {
    event.preventDefault();
    const entries = grants.split('\n').map(value => value.trim()).filter(Boolean).map(value => {
      const [subject, capabilities] = value.split('=', 2);
      return { subject: subject.trim(), capabilities: capabilities?.split(',').map(item => item.trim()).filter(Boolean) || requested };
    });
    submit({ label: name, owner, profile, grants: entries, enabled: true });
  }
  return <details className="enrollment" open={!device.enabled || undefined}>
    <summary>{device.enabled ? 'Enrollment and agent access' : 'Enroll this device'}</summary>
    <form onSubmit={onboard}>
      <div className="fields"><label>Device name<input required maxLength={128} value={name} onChange={event => setName(event.target.value)} /></label>
        <label>Owner identity<input required value={owner} onChange={event => setOwner(event.target.value)} /></label>
        <label>Access profile<select value={profile} onChange={event => setProfile(event.target.value as 'personal' | 'dedicated')}><option value="personal">Personal · approval required</option>{device.mode !== 'companion' && <option value="dedicated">Dedicated · agent grants</option>}</select></label>
      </div>
      <label>Agent grants<textarea rows={2} placeholder={`${actor.workspace === 'local' ? 'local-agent' : 'agent-identity'}=screen,ui_tree,touch`} value={grants} onChange={event => setGrants(event.target.value)} /></label>
      <p className="hint">Use one identity per line, optionally followed by <code>=</code> and a list of permitted capabilities.</p>
      <div className="actions"><button className="primary" disabled={busy}>{device.enabled ? 'Save access' : 'Enroll device'}</button>{device.enabled && !confirmRevoke && <button type="button" className="danger" disabled={busy} onClick={() => setConfirmRevoke(true)}>Revoke enrollment</button>}</div>
      {device.enabled && confirmRevoke && <div className="confirm-revoke" role="group" aria-label="Confirm enrollment revocation">
        <p>Revoking ends active sessions and removes agent access.</p>
        <div className="actions"><button type="button" disabled={busy} onClick={() => setConfirmRevoke(false)}>Cancel</button>
          <button type="button" className="danger" disabled={busy} onClick={() => { setConfirmRevoke(false); submit({ label: device.label, owner: device.owner, profile: device.profile, grants: device.grants, enabled: false }); }}>Confirm revoke</button></div>
      </div>}
    </form>
  </details>;
}

function StartForm({ device, busy, submit }: { device: Device; busy: boolean; submit: (value: unknown) => void }) {
  const [purpose, setPurpose] = useState('');
  const options = Object.keys(device.capabilities).filter(cap => device.capabilities[cap]);
  const [selected, setSelected] = useState(options.filter(cap => ['screen', 'touch', 'ui_tree', 'text'].includes(cap)));
  return <details className="enrollment session-start request-session">
    <summary>Request a session</summary>
    <form onSubmit={event => { event.preventDefault(); submit({ device_id: device.id, purpose, capabilities: selected }); }}>
      <label>Purpose<input minLength={3} maxLength={256} required value={purpose} onChange={event => setPurpose(event.target.value)} /></label>
      <fieldset><legend>Capabilities</legend><div className="cap-options">{options.map(cap => <label key={cap}><input type="checkbox" checked={selected.includes(cap)} onChange={event => setSelected(current => event.target.checked ? [...current, cap] : current.filter(item => item !== cap))} />{labels[cap] || cap}</label>)}</div></fieldset>
      <div className="actions"><button className="primary" disabled={busy || selected.length === 0}>Request access</button></div>
    </form>
  </details>;
}

function MaintenanceForm({ device, token, session, onAttempt }: { device: Device; token: string; session: Session; onAttempt: () => void }) {
  const items = device.catalog || [];
  const [artifact, setArtifact] = useState(items[0]?.id || '');
  const [verb, setVerb] = useState<'install' | 'reset'>('install');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');
  const [error, setError] = useState('');
  const phrase = `${verb} ${artifact}`;

  async function apply(event: FormEvent) {
    event.preventDefault();
    if (confirm !== phrase || busy) return;
    setBusy(true);
    setNotice('');
    setError('');
    setConfirm('');
    onAttempt();
    try {
      const result = await api<{ state: string; error?: { message: string } }>(token, `/v1/sessions/${session.id}/operations`, 'POST', {
        id: crypto.randomUUID(), kind: verb === 'install' ? 'app_install' : 'app_reset', maintenance: { artifact_id: artifact, confirm },
      });
      if (result.state !== 'succeeded') throw new Error(result.error?.message || 'The phone did not confirm the operation.');
      setNotice(verb === 'install' ? 'The APK was installed.' : 'The app data was cleared.');
    } catch (error) { setError(errorText(error)); } finally { setBusy(false); }
  }

  return <details className="enrollment session-start">
    <summary>App maintenance</summary>
    <form onSubmit={apply}>
      <div className="fields"><label>Approved app<select disabled={busy} value={artifact} onChange={event => { setArtifact(event.target.value); setConfirm(''); setNotice(''); setError(''); }}>
        {items.map(item => <option value={item.id} key={item.id}>{item.label}</option>)}
      </select></label><label>Operation<select disabled={busy} value={verb} onChange={event => { setVerb(event.target.value as 'install' | 'reset'); setConfirm(''); setNotice(''); setError(''); }}>
        <option value="install">Install APK</option><option value="reset">Reset app data</option>
      </select></label></div>
      {verb === 'reset' && <p className="hint">Reset clears this app’s data on the selected device.</p>}
      <label><span>Type <code>{phrase}</code> to confirm</span><input autoComplete="off" autoCapitalize="none" spellCheck={false} disabled={busy} value={confirm} onChange={event => setConfirm(event.target.value)} /></label>
      <div className="actions"><button className={verb === 'reset' ? 'danger' : 'primary'} disabled={busy || confirm !== phrase}>{busy ? 'Working…' : verb === 'install' ? 'Install APK' : 'Reset app data'}</button></div>
      {notice && <p className="hint" role="status">{notice}</p>}
      {error && <p className="notice" role="alert">{error}</p>}
    </form>
  </details>;
}

function Viewer({ token, actor, session, supportsVideo, captureRevision, onError }: { token: string; actor: Actor; session: Session; supportsVideo: boolean; captureRevision?: number; onError: (message: string) => void }) {
  const [frame, setFrame] = useState<Observation>();
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState(false);
  const [live, setLive] = useState(false);
  const start = useRef<{ x: number; y: number } | undefined>(undefined);
  const base = `/v1/sessions/${session.id}`;
  const owner = session.owner === actor.subject && actor.role !== 'agent';
  const controller = !session.paused && session.controller === actor.subject && session.capabilities.includes('touch');

  const observe = useCallback(async () => {
    for (let attempt = 0; ; attempt += 1) {
      const result = await api<{ observation?: Observation; error?: { code: string; message: string } }>(token, `${base}/operations`, 'POST', { kind: 'observe', observe: { screen: true, tree: false } });
      if (!result.error) return result.observation;
      if (attempt >= 2 || !['DEVICE_TRANSITION', 'STALE_REFERENCE', 'OPERATION_IN_PROGRESS'].includes(result.error.code)) {
        throw new Error(result.error.message);
      }
      await new Promise(resolve => setTimeout(resolve, 300));
    }
  }, [token, base]);

  useEffect(() => {
    if (!owner || session.profile !== 'personal') return;
    const presence = () => { if (!document.hidden) api(token, `${base}/presence`, 'POST').catch(() => undefined); };
    presence();
    const timer = window.setInterval(presence, 4500);
    document.addEventListener('visibilitychange', presence);
    return () => { window.clearInterval(timer); document.removeEventListener('visibilitychange', presence); };
  }, [base, owner, session.profile, token]);

  useEffect(() => {
    if (!preview || busy || controller || live || session.paused) return;
    let closed = false;
    let timer = 0;
    const update = async () => {
      try {
        if (!document.hidden && !closed) {
          const next = await observe();
          if (!closed) setFrame(next);
        }
      } catch {
        // An agent can temporarily occupy the device worker. The next refresh retries observation.
      } finally {
        if (!closed) timer = window.setTimeout(update, 2500);
      }
    };
    void update();
    return () => { closed = true; window.clearTimeout(timer); };
  }, [preview, busy, controller, live, session.paused, observe]);

  useEffect(() => { if (session.paused || !supportsVideo) setLive(false); }, [session.paused, supportsVideo]);

  useEffect(() => {
    start.current = undefined;
    setFrame(undefined);
  }, [captureRevision, session.paused]);

  async function perform(action?: Record<string, unknown>) {
    if (busy) return;
    setBusy(true);
    onError('');
    try {
      if (action && frame) {
        const result = await api<{ error?: { message: string } }>(token, `${base}/operations`, 'POST', { id: crypto.randomUUID(), kind: 'act', action: { ...action, frame_id: frame.frame_id } });
        if (result.error) throw new Error(result.error.message);
      }
      setFrame(await observe());
    } catch (error) { onError(errorText(error)); } finally { setBusy(false); }
  }

  const position = (event: PointerEvent<HTMLImageElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    const clamp = (value: number) => Math.min(0.9999, Math.max(0, value));
    return { x: clamp((event.clientX - rect.left) / rect.width), y: clamp((event.clientY - rect.top) / rect.height) };
  };

  const show = session.capabilities.includes('screen') && (owner || session.requester === actor.subject || controller);
  if (!show) return null;
  return <section className="viewer" aria-label="Phone screen">
    <div className="viewer-header"><div><h3>Phone screen</h3><p>{frame ? `Captured at ${time(frame.captured_at)}` : `Controlled by ${session.controller}`}</p></div><div className="actions">
      {supportsVideo && !session.paused && <button type="button" aria-pressed={live} onClick={() => setLive(value => !value)}>{live ? 'Stop live video' : 'Live video'}</button>}
      {!live && !controller && !session.paused && <button type="button" aria-pressed={preview} onClick={() => setPreview(value => !value)}>{preview ? 'Pause preview' : 'Auto-refresh'}</button>}
      {!live && <button type="button" onClick={() => perform()} disabled={busy}>{busy ? 'Working…' : 'Capture screen'}</button>}
    </div></div>
    <div className="viewer-screen">{live && !session.paused ? <LiveVideo token={token} path={`${base}/video`} /> : frame?.image ? <img draggable="false" alt="Latest authorized screenshot of the phone" className={controller ? 'interactive' : ''} src={`data:${frame.image.mime};base64,${frame.image.data}`}
      onPointerDown={event => { if (controller && !busy) { event.currentTarget.setPointerCapture(event.pointerId); start.current = position(event); } }}
      onPointerCancel={() => { start.current = undefined; }}
      onPointerUp={event => {
        if (!controller || !start.current) return;
        const point = start.current;
        start.current = undefined;
        const end = position(event);
        if (Math.hypot(point.x - end.x, point.y - end.y) > 0.04) perform({ type: 'swipe', point, end });
        else perform({ type: 'tap', point });
      }} /> : <div className="screen-empty"><PhoneMark /><span>Capture a screen to inspect the session.</span></div>}</div>
    {controller && frame && !live && <div className="viewer-keys"><button type="button" onClick={() => perform({ type: 'key', key: 'home' })} disabled={busy}>Home</button>
      {session.capabilities.includes('text') && <form onSubmit={event => { event.preventDefault(); perform({ type: 'type', text }); setText(''); }}><input aria-label="Text to type on the phone" value={text} onChange={event => setText(event.target.value)} /><button disabled={busy || !text}>Type</button></form>}</div>}
  </section>;
}

createRoot(document.getElementById('root')!).render(<App />);
