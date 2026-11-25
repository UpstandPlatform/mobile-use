export type Capability = 'screen' | 'ui_tree' | 'touch' | 'text' | 'apps' | 'browser' | 'camera' | 'microphone' | 'multi_touch';
export type Device = {
  id: string;
  label: string;
  platform: 'android' | 'ios';
  mode: string;
  online: boolean;
  virtual: boolean;
  capabilities: Partial<Record<Capability, boolean>>;
  live_video?: boolean;
  catalog?: { id: string; label: string; app_id: string }[];
};
export type Session = {
  id: string;
  device_id: string;
  state: 'pending_approval' | 'active' | 'denied' | 'expired' | 'ended';
  purpose: string;
  capabilities: Capability[];
  expires_at: string;
};
export type Point = { x: number; y: number };
type Target = { point: Point; node_id?: never } | { node_id: string; point?: never };
type Action = { frame_id: string } & (
  | ({ type: 'tap' | 'long_press'; duration_ms?: number } & Target)
  | { type: 'swipe' | 'drag'; duration_ms?: number; point: Point; end: Point }
  | { type: 'pinch'; duration_ms?: number; point: Point; end: Point; second_point: Point; second_end: Point }
  | { type: 'type'; text: string }
  | { type: 'key'; key: 'home' | 'back' | 'overview' | 'enter' | 'backspace' | 'volume_up' | 'volume_down' }
);
type BrowserAction = { context_id: string; document_id: string } & (
  | { type: 'navigate'; url: string }
  | { type: 'click'; selector: string }
  | { type: 'fill'; selector: string; text: string }
  | { type: 'scroll'; delta_y: number }
);
type SensorType = 'camera' | 'microphone';
export type Operation =
  | { kind: 'observe'; observe: { screen: boolean; tree: boolean; wait_for?: string; timeout_ms?: number } }
  | { kind: 'act'; id: string; action: Action }
  | { kind: 'apps_list' | 'browser_contexts' }
  | { kind: 'app_launch' | 'app_terminate'; id: string; app_id: string }
  | { kind: 'app_install' | 'app_reset'; id: string; maintenance: { artifact_id: string; confirm: string } }
  | { kind: 'browser_observe'; browser: { context_id: string } }
  | { kind: 'browser_act'; id: string; browser: BrowserAction }
  | { kind: 'sensor_request'; id: string; sensor: { type: SensorType; duration_seconds?: number } }
  | { kind: 'sensor_status' | 'artifact_get'; sensor: { type: SensorType; request_id: string } };
export type Fault = { code: string; message: string };
export type Artifact = { mime: 'image/png' | 'image/jpeg' | 'audio/wav'; data: string };
export type Result = {
  id: string;
  state: 'succeeded' | 'failed' | 'executing' | 'outcome_unknown';
  observation?: {
    frame_id: string;
    captured_at: string;
    width: number;
    height: number;
    app_id?: string;
    image?: Artifact;
    nodes?: { id: string; label?: string; resource_id?: string }[];
  };
  apps?: string[];
  contexts?: { id: string; title?: string; url?: string }[];
  page?: { context_id: string; document_id: string; title: string; url: string; elements: unknown[] };
  sensor?: { id: string; type: SensorType; state: string; expires_at: string };
  artifact?: Artifact;
  error?: Fault;
};

export class APIError extends Error {
  constructor(public readonly status: number, public readonly code: string, message: string) { super(message); }
}

export class MobileUse {
  readonly #origin: string;
  readonly #token: string;

  constructor(origin: string, token: string) {
    const url = new URL(origin);
    if (url.username || url.password || url.pathname !== '/' || url.search || url.hash) throw new Error('Use a bare mobile-use origin.');
    const loopback = ['localhost', '127.0.0.1', '[::1]'].includes(url.hostname);
    if (url.protocol !== 'https:' && !(url.protocol === 'http:' && loopback)) throw new Error('Remote origins require HTTPS.');
    this.#origin = url.origin;
    this.#token = token;
  }

  devices() { return this.request<Device[]>('GET', '/v1/devices'); }

  start(device_id: string, purpose: string, capabilities: Capability[]) {
    return this.request<Session>('POST', '/v1/sessions', { device_id, purpose, capabilities });
  }

  status(sessionID: string) { return this.request<Session>('GET', this.path(sessionID)); }

  operate(sessionID: string, operation: Operation) {
    const timeout = operation.kind === 'app_install' || operation.kind === 'app_reset' ? 24000 : 15000;
    return this.request<Result>('POST', `${this.path(sessionID)}/operations`, operation, timeout);
  }

  end(sessionID: string) { return this.request<Session>('POST', `${this.path(sessionID)}/end`); }

  private path(id: string) { return `/v1/sessions/${encodeURIComponent(id)}`; }

  private async request<T>(method: string, path: string, value?: unknown, timeout = 15000): Promise<T> {
    const headers = new Headers({ Authorization: `Bearer ${this.#token}` });
    if (value !== undefined) headers.set('Content-Type', 'application/json');
    const response = await fetch(this.#origin + path, {
      method, headers, redirect: 'error', signal: AbortSignal.timeout(timeout),
      body: value === undefined ? undefined : JSON.stringify(value),
    });
    const data = await response.json();
    if (!response.ok) throw new APIError(response.status, data.error?.code ?? 'HTTP_ERROR', data.error?.message ?? response.statusText);
    return data as T;
  }
}
