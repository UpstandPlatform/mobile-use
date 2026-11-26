export type Actor = { workspace: string; subject: string; role: string };
export type Grant = { subject: string; capabilities: string[] };
export type Device = {
  id: string;
  label: string;
  platform: 'android' | 'ios';
  mode: string;
  virtual: boolean;
  capabilities: Record<string, boolean>;
  live_video: boolean;
  catalog?: { id: string; label: string; app_id: string }[];
  connector?: string;
  owner?: string;
  profile: '' | 'personal' | 'dedicated';
  grants?: Grant[];
  online: boolean;
  enabled: boolean;
};
export type Session = {
  id: string;
  device_id: string;
  requester: string;
  controller: string;
  owner: string;
  purpose: string;
  state: 'pending_approval' | 'active' | 'ended' | 'denied' | 'expired';
  capabilities: string[];
  profile: string;
  expires_at: string;
  paused?: boolean;
};
export type Observation = {
  frame_id: string;
  captured_at: string;
  width: number;
  height: number;
  orientation?: string;
  app_id?: string;
  image?: { mime: string; data: string };
  nodes?: UINode[];
  partial?: string[];
};
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
export type Audit = { at: string; actor: string; event: string; device?: string; session?: string; code?: string };
export type AuditPage = { items: Audit[]; page: number; page_size: number; total: number; total_pages: number };
export type OIDC = { issuer: string; client_id: string; authorization_endpoint: string; token_endpoint: string; resource: string; companion_enabled: boolean; local_exchange?: boolean };
export type CompanionEndpoint = { origin: string; interface?: string; primary?: boolean };
export type SystemInfo = {
  version?: string;
  mode: 'workstation' | 'remote';
  host_name?: string;
  origin: string;
  adb_available?: boolean;
  adb_error?: string;
  wireless_adb_supported?: boolean;
  companion_enabled: boolean;
  companion_mode?: 'lan' | 'remote';
  companion_origin?: string;
  companion_origins?: CompanionEndpoint[];
  companion_fingerprint?: string;
  companion_fingerprint_short?: string;
  mdns_active?: boolean;
  mcp_executable?: string;
  mcp_commands?: { codex: string; claude: string; http: string };
};
export type PairingSession = {
  id: string;
  code: string;
  origin: string;
  origins?: CompanionEndpoint[];
  fingerprint?: string;
  mode?: 'lan' | 'remote';
  host_name?: string;
  qr_uri: string;
  expires_at: string;
  auto_enroll?: boolean;
};
export type PairingStatus = {
  id: string;
  state: 'waiting' | 'paired' | 'expired' | 'cancelled';
  device_id?: string;
  label?: string;
  auto_enroll?: boolean;
  auto_enrolled?: boolean;
  auto_enroll_error?: string;
  expires_at: string;
};
export type WirelessAdbService = { name: string; type: 'pairing' | 'connect'; address: string };
export type WirelessAdbDiscovery = {
  adb_available: boolean;
  adb_error?: string;
  wireless_services: WirelessAdbService[];
  connected_count: number;
};

export async function exchangeLaunchTicket(ticket: string): Promise<string> {
  const response = await fetch('/auth/local-exchange', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ticket }),
    redirect: 'error',
  });
  const value = await response.json();
  if (!response.ok || typeof value.token !== 'string') {
    throw new Error(value.error?.message || 'The desktop launch ticket could not be verified.');
  }
  return value.token;
}

export const labels: Record<string, string> = {
  screen: 'Screen', ui_tree: 'UI tree', touch: 'Touch', text: 'Text input', apps: 'Installed apps',
  browser: 'Browser', camera: 'Camera', microphone: 'Microphone', multi_touch: 'Multi-touch',
  app_management: 'Approved app install/reset',
};

export class ApiError extends Error {
  constructor(
    message: string,
    readonly status: number,
    readonly code?: string,
  ) {
    super(message);
    this.name = 'ApiError';
  }
}

export async function api<T>(
  token: string,
  path: string,
  method = 'GET',
  body?: unknown,
  signal?: AbortSignal,
): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: { Authorization: `Bearer ${token}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    redirect: 'error',
    signal,
  });
  if (response.status === 204) return undefined as T;
  const text = await response.text();
  let value: unknown;
  try {
    value = text ? JSON.parse(text) : undefined;
  } catch {
    if (!response.ok) throw new ApiError(`Request failed (${response.status})`, response.status);
    throw new ApiError('The server returned an unreadable response.', response.status);
  }
  const fault = (value as { error?: { message?: string; code?: string } } | undefined)?.error;
  if (!response.ok) {
    throw new ApiError(fault?.message || `Request failed (${response.status})`, response.status, fault?.code);
  }
  return value as T;
}

function base64url(bytes: Uint8Array): string {
  return btoa(String.fromCharCode(...bytes)).replaceAll('+', '-').replaceAll('/', '_').replaceAll('=', '');
}

export async function beginLogin(config: OIDC): Promise<void> {
  const verifier = base64url(crypto.getRandomValues(new Uint8Array(48)));
  const state = base64url(crypto.getRandomValues(new Uint8Array(24)));
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier));
  sessionStorage.setItem('mobile-use.oauth', JSON.stringify({ verifier, state }));
  const endpoint = new URL(config.authorization_endpoint);
  const params = new URLSearchParams({
    client_id: config.client_id,
    response_type: 'code',
    scope: 'openid mobile-use',
    redirect_uri: `${location.origin}/auth/callback`,
    code_challenge: base64url(new Uint8Array(digest)),
    code_challenge_method: 'S256',
    state,
  });
  if (config.resource.startsWith('https://')) params.set('resource', config.resource);
  endpoint.search = params.toString();
  location.assign(endpoint);
}

export async function completeLogin(config: OIDC): Promise<string> {
  const returned = new URLSearchParams(location.search);
  const saved = sessionStorage.getItem('mobile-use.oauth');
  sessionStorage.removeItem('mobile-use.oauth');
  history.replaceState(null, '', '/');
  if (!saved || !returned.has('code')) throw new Error('The sign-in could not be completed.');
  const { verifier, state } = JSON.parse(saved) as { verifier: string; state: string };
  if (returned.get('state') !== state || (returned.has('iss') && returned.get('iss') !== config.issuer)) {
    throw new Error('The sign-in response could not be verified.');
  }
  const body = new URLSearchParams({
    grant_type: 'authorization_code',
    client_id: config.client_id,
    redirect_uri: `${location.origin}/auth/callback`,
    code: returned.get('code')!,
    code_verifier: verifier,
  });
  if (config.resource.startsWith('https://')) body.set('resource', config.resource);
  const response = await fetch(config.token_endpoint, { method: 'POST', body, redirect: 'error' });
  const result = await response.json();
  if (!response.ok || typeof result.access_token !== 'string') throw new Error('The identity provider did not issue an access token.');
  return result.access_token;
}
