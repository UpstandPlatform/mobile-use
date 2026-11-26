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
export type TouchPath = { start: Point; end: Point };
export type UINode = {
  id: string;
  parent_id?: string;
  role?: string;
  label?: string;
  hint?: string;
  resource_id?: string;
  bounds: { x: number; y: number; width: number; height: number };
  enabled: boolean;
  visible: boolean;
  clickable: boolean;
  long_clickable?: boolean;
  focusable?: boolean;
  editable?: boolean;
  checkable?: boolean;
  checked?: boolean;
  selected?: boolean;
  scrollable?: boolean;
  protected?: boolean;
  focused?: boolean;
};
type PointTarget = { point: Point; node_id?: never };
type NodeTarget = { node_id: string; point?: never };
export type Action =
  | ({ frame_id: string; type: 'tap' | 'double_tap' | 'long_press'; duration_ms?: number } &
      (PointTarget | NodeTarget | { paths: [TouchPath] }))
  | ({ frame_id: string; type: 'swipe' | 'drag' | 'fling'; duration_ms?: number } &
      ({ point: Point; end: Point } | { paths: [TouchPath] }))
  | ({ frame_id: string; type: 'scroll'; duration_ms?: number; direction?: 'up' | 'down' | 'left' | 'right' } &
      ({ point: Point; end: Point } | { node_id: string } | { paths: [TouchPath] }))
  | ({ frame_id: string; type: 'pinch' | 'rotate'; duration_ms?: number } &
      ({ paths: [TouchPath, TouchPath] } | { point: Point; end: Point; second_point: Point; second_end: Point }))
  | ({ frame_id: string; type: 'two_finger_tap'; duration_ms?: number } &
      ({ paths: [TouchPath, TouchPath] } | { point: Point; end: Point; second_point: Point; second_end: Point }))
  | { frame_id: string; type: 'multi_touch'; duration_ms?: number; paths: [TouchPath, TouchPath, ...TouchPath[]] }
  | { frame_id: string; type: 'type'; text: string }
  | { frame_id: string; type: 'key'; key: NavigationKey };
export type NavigationKey =
  | 'home' | 'back' | 'overview' | 'enter' | 'backspace' | 'tab' | 'escape' | 'menu'
  | 'dpad_up' | 'dpad_down' | 'dpad_left' | 'dpad_right' | 'volume_up' | 'volume_down'
  | 'volume_mute' | 'notification_shade' | 'quick_settings';
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
    orientation?: string;
    nodes?: UINode[];
    partial?: string[];
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
