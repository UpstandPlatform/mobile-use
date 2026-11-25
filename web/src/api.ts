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
  app_id?: string;
  image?: { mime: string; data: string };
  partial?: string[];
};
export type Audit = { at: string; actor: string; event: string; device?: string; session?: string; code?: string };
export type OIDC = { issuer: string; client_id: string; authorization_endpoint: string; token_endpoint: string; resource: string; companion_enabled: boolean };

export const labels: Record<string, string> = {
  screen: 'Screen', ui_tree: 'UI tree', touch: 'Touch', text: 'Text input', apps: 'Installed apps',
  browser: 'Browser', camera: 'Camera', microphone: 'Microphone', multi_touch: 'Multi-touch',
};

export async function api<T>(token: string, path: string, method = 'GET', body?: unknown): Promise<T> {
  const response = await fetch(path, {
    method,
    headers: { Authorization: `Bearer ${token}`, ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    redirect: 'error',
  });
  if (response.status === 204) return undefined as T;
  const value = await response.json();
  if (!response.ok) throw new Error(value.error?.message || `Request failed (${response.status})`);
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
