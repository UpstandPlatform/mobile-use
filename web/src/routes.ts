import type { ConsolePage } from './AppSidebar';

export type ConsoleRoute = {
  page: ConsolePage;
  deviceId: string;
  auditPage: number;
  auditPageSize: number;
};

const pagesByPath: Record<string, ConsolePage> = {
  '/': 'Overview',
  '/devices': 'Devices',
  '/sessions': 'Sessions',
  '/agent-setup': 'Agent setup',
  '/downloads': 'Downloads',
  '/audit': 'Audit',
};

const pathsByPage: Record<ConsolePage, string> = {
  Overview: '/',
  Devices: '/devices',
  Sessions: '/sessions',
  'Agent setup': '/agent-setup',
  Downloads: '/downloads',
  Audit: '/audit',
};

export function readConsoleRoute(pathname = location.pathname, search = location.search): ConsoleRoute {
  let path = pathname;
  try {
    path = decodeURIComponent(pathname);
  } catch {
    path = '/';
  }
  const deviceMatch = /^\/devices\/([a-zA-Z0-9_-]+)$/.exec(path);
  const page = deviceMatch ? 'Devices' : pagesByPath[path] || 'Overview';
  const params = new URLSearchParams(search);
  const pageValue = Number(params.get('page'));
  const sizeValue = Number(params.get('page_size'));
  const allowedSizes = [10, 25, 50, 100];
  return {
    page,
    deviceId: deviceMatch?.[1] || '',
    auditPage: Number.isSafeInteger(pageValue) && pageValue > 0 ? pageValue : 1,
    auditPageSize: allowedSizes.includes(sizeValue) ? sizeValue : 25,
  };
}

export function consolePath(
  page: ConsolePage,
  options: { deviceId?: string; auditPage?: number; auditPageSize?: number } = {},
): string {
  const path = page === 'Devices' && options.deviceId
    ? `/devices/${encodeURIComponent(options.deviceId)}`
    : pathsByPage[page];
  const params = new URLSearchParams();
  if (page === 'Audit') {
    if (options.auditPage && options.auditPage > 1) params.set('page', String(options.auditPage));
    if (options.auditPageSize && options.auditPageSize !== 25) {
      params.set('page_size', String(options.auditPageSize));
    }
  }
  const query = params.toString();
  return query ? `${path}?${query}` : path;
}
