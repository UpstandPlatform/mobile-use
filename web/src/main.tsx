import { lazy, Suspense, useCallback, useEffect, useRef, useState } from 'react';
import type { FormEvent, PointerEvent } from 'react';
import { createRoot } from 'react-dom/client';
import {
  Activity,
  AlertCircle,
  ArrowRight,
  Check,
  CircleHelp,
  Moon,
  RefreshCw,
  ShieldCheck,
  Smartphone,
  Sun,
  Trash2,
} from 'lucide-react';
import { ThemeProvider, useTheme } from 'next-themes';
import { toast } from 'sonner';
import { api, ApiError, beginLogin, completeLogin, exchangeLaunchTicket, labels } from './api';
import type { Actor, Audit, AuditPage, Device, Grant, Observation, OIDC, Session, SystemInfo } from './api';
import { AddDevicePanel } from './AddDevicePanel';
import { CompanionSensors, LiveVideo, MediaVideo, useLiveMedia } from './LiveVideo';
import { consolePath, readConsoleRoute } from './routes';
const McpSetup = lazy(() => import('./McpSetup').then((module) => ({ default: module.McpSetup })));
import { AppSidebar } from './AppSidebar';
import type { ConsolePage } from './AppSidebar';
const Downloads = lazy(() => import('./Downloads').then((module) => ({ default: module.Downloads })));
import { Overview } from './Overview';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from '@/components/ui/alert-dialog';
import { Badge } from '@/components/ui/badge';
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage } from '@/components/ui/breadcrumb';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty';
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { ScrollArea } from '@/components/ui/scroll-area';
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Separator } from '@/components/ui/separator';
import { Sheet, SheetContent, SheetDescription, SheetHeader, SheetTitle } from '@/components/ui/sheet';
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/components/ui/sidebar';
import { Skeleton } from '@/components/ui/skeleton';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { Textarea } from '@/components/ui/textarea';
import { TooltipProvider } from '@/components/ui/tooltip';
import { Toaster } from '@/components/ui/sonner';
import {
  Pagination,
  PaginationContent,
  PaginationItem,
  PaginationLink,
  PaginationNext,
  PaginationPrevious,
} from '@/components/ui/pagination';
import './index.css';

type Tab = ConsolePage;
type ScreenPointer = { start: { x: number; y: number }; end: { x: number; y: number }; released: boolean };
type TouchMode = 'direct' | 'tap' | 'double_tap' | 'long_press' | 'swipe' | 'drag' | 'scroll' | 'fling';
type AndroidKey = 'enter' | 'tab' | 'escape' | 'menu' | 'dpad_up' | 'dpad_down' | 'dpad_left' | 'dpad_right' | 'volume_up' | 'volume_down' | 'volume_mute';
const time = (date: string) => new Date(date).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
const errorText = (error: unknown) => (error instanceof Error ? error.message : 'Something went wrong.');

function auditPageNumbers(current: number, total: number) {
  if (total <= 0) return [];
  const count = Math.min(5, total);
  const start = Math.max(1, Math.min(current - Math.floor(count / 2), total - count + 1));
  return Array.from({ length: count }, (_, index) => start + index);
}

function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem('mobile-use.token') || '');
  const [draftToken, setDraftToken] = useState('');
  const [oidc, setOIDC] = useState<OIDC>();
  const [authConfigLoading, setAuthConfigLoading] = useState(true);
  const [identityLoading, setIdentityLoading] = useState(() => Boolean(sessionStorage.getItem('mobile-use.token')));
  const [systemInfo, setSystemInfo] = useState<SystemInfo>();
  const [actor, setActor] = useState<Actor>();
  const [devices, setDevices] = useState<Device[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [tab, setTab] = useState<Tab>(() => readConsoleRoute().page);
  const [selected, setSelected] = useState(() => readConsoleRoute().deviceId);
  const [inventoryState, setInventoryState] = useState<'loading' | 'ready' | 'error'>('loading');
  const [audits, setAudits] = useState<AuditPage>();
  const [auditPage, setAuditPage] = useState(() => readConsoleRoute().auditPage);
  const [auditPageSize, setAuditPageSize] = useState(() => readConsoleRoute().auditPageSize);
  const [auditState, setAuditState] = useState<'idle' | 'loading' | 'ready' | 'error'>('idle');
  const [auditError, setAuditError] = useState('');
  const [auditReload, setAuditReload] = useState(0);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [lastRefreshedAt, setLastRefreshedAt] = useState<Date>();
  const [captureRevision, setCaptureRevision] = useState(0);
  const [showAddDevice, setShowAddDevice] = useState(false);
  const [removeTarget, setRemoveTarget] = useState<Device>();
  const refreshSequence = useRef(0);

  const signedIn = useCallback((value: string) => {
    sessionStorage.setItem('mobile-use.token', value);
    setToken(value);
    setIdentityLoading(true);
    setDraftToken('');
  }, []);

  useEffect(() => {
    const hash = location.hash;
    const launchTicket = hash.startsWith('#launch=') ? hash.slice('#launch='.length) : '';
    if (launchTicket) history.replaceState(null, '', `${location.pathname}${location.search}`);

    fetch('/auth/config')
      .then((response) => {
        if (!response.ok || !response.headers.get('content-type')?.includes('application/json')) {
          throw new Error(
            'The mobile-use service is not available at this address. Start it with `mobile-use up`, then open its console.',
          );
        }
        return response.json() as Promise<OIDC>;
      })
      .then(async (value: OIDC) => {
        setOIDC(value);
        if (location.pathname === '/auth/callback') {
          signedIn(await completeLogin(value));
        } else if (launchTicket && value.local_exchange) {
          signedIn(await exchangeLaunchTicket(launchTicket));
        }
      })
      .catch((error) => setError(errorText(error)))
      .finally(() => setAuthConfigLoading(false));
  }, [signedIn]);

  const refresh = useCallback(
    async (credential: string) => {
      const sequence = ++refreshSequence.current;
      setInventoryState((current) => current === 'ready' ? current : 'loading');
      try {
        const [nextDevices, nextSessions] = await Promise.all([
          api<Device[]>(credential, '/v1/devices'),
          api<Session[]>(credential, '/v1/sessions'),
        ]);
        if (sequence !== refreshSequence.current) return;
        setDevices(nextDevices);
        setSessions(nextSessions);
        setSelected((current) =>
          nextDevices.some((device) => device.id === current) ? current : nextDevices[0]?.id || '',
        );
        setInventoryState('ready');
      } catch (error) {
        if (sequence !== refreshSequence.current) return;
        setInventoryState((current) => current === 'ready' ? 'ready' : 'error');
        throw error;
      }
    },
    [],
  );

  useEffect(() => {
    if (!token) {
      setIdentityLoading(false);
      return;
    }
    let ignore = false;
    setIdentityLoading(true);
    api<Actor>(token, '/v1/me')
      .then(async (identity) => {
        if (ignore) return;
        setActor(identity);
        setIdentityLoading(false);
        api<SystemInfo>(token, '/v1/system/info')
          .then((info) => {
            if (!ignore) setSystemInfo(info);
          })
          .catch(() => undefined);
        // A refresh failure is not an authentication failure and should preserve the session.
        await refresh(token).catch((error) => {
          if (!ignore) setError(errorText(error));
        });
      })
      .catch((error) => {
        if (!ignore) {
          setActor(undefined);
          setIdentityLoading(false);
          if (error instanceof ApiError && [401, 403].includes(error.status)) {
            sessionStorage.removeItem('mobile-use.token');
            setToken('');
          }
          setError(errorText(error));
        }
      });
    return () => {
      ignore = true;
    };
  }, [token, refresh]);

  useEffect(() => {
    if (!token || !actor) return;
    const timer = window.setInterval(() => {
      if (!document.hidden) refresh(token).catch((error) => setError(errorText(error)));
    }, 3500);
    return () => window.clearInterval(timer);
  }, [actor, token, refresh]);

  useEffect(() => {
    function normalizeCurrentRoute() {
      const route = readConsoleRoute();
      const canonical = consolePath(route.page, {
        deviceId: route.deviceId,
        auditPage: route.auditPage,
        auditPageSize: route.auditPageSize,
      });
      if (`${location.pathname}${location.search}` !== canonical) {
        history.replaceState(null, '', canonical);
      }
    }
    function onPopState() {
      const route = readConsoleRoute();
      setTab(route.page);
      setSelected(route.deviceId);
      setAuditPage(route.auditPage);
      setAuditPageSize(route.auditPageSize);
      setShowAddDevice(false);
      normalizeCurrentRoute();
    }
    normalizeCurrentRoute();
    window.addEventListener('popstate', onPopState);
    return () => window.removeEventListener('popstate', onPopState);
  }, []);

  useEffect(() => {
    if (!actor || tab !== 'Audit' || !['admin', 'auditor'].includes(actor.role)) return;
    const controller = new AbortController();
    setAuditState('loading');
    setAuditError('');
    api<AuditPage>(
      token,
      `/v1/audit/page?page=${auditPage}&page_size=${auditPageSize}`,
      'GET',
      undefined,
      controller.signal,
    )
      .then((value) => {
        const lastPage = Math.max(1, value.total_pages);
        if (auditPage > lastPage) {
          setAuditPage(lastPage);
          history.replaceState(null, '', consolePath('Audit', { auditPage: lastPage, auditPageSize }));
          return;
        }
        setAudits(value);
        setAuditState('ready');
      })
      .catch((reason) => {
        if (controller.signal.aborted) return;
        setAuditError(errorText(reason));
        setAuditState('error');
      });
    return () => controller.abort();
  }, [actor, auditPage, auditPageSize, auditReload, tab, token]);

  useEffect(() => {
    if (tab === 'Audit' && actor && !['admin', 'auditor'].includes(actor.role)) {
      history.replaceState(null, '', consolePath('Overview'));
      setTab('Overview');
    }
  }, [actor, tab]);

  useEffect(() => {
    if (tab === 'Devices' && devices.length && !devices.some((item) => item.id === selected)) {
      const next = devices[0].id;
      setSelected(next);
      history.replaceState(null, '', consolePath('Devices', { deviceId: next }));
    }
  }, [devices, selected, tab]);

  async function run<T>(task: () => Promise<T>, success?: string): Promise<T | undefined> {
    setBusy(true);
    setError('');
    try {
      const result = await task();
      if (success) toast.success(success);
      if (actor) {
        try {
          await refresh(token);
        } catch {
          const message = 'The action completed, but the workspace could not refresh. Try Refresh again.';
          setError(message);
          toast.error(message);
        }
      }
      return result;
    } catch (error) {
      setError(errorText(error));
      toast.error(errorText(error));
      return undefined;
    } finally {
      setBusy(false);
    }
  }

  async function refreshWorkspace() {
    setBusy(true);
    setError('');
    try {
      await refresh(token);
      setLastRefreshedAt(new Date());
      if (tab === 'Audit') setAuditReload((value) => value + 1);
    } catch (reason) {
      const message = errorText(reason);
      setError(message);
      toast.error(message);
    } finally {
      setBusy(false);
    }
  }

  function signOut() {
    sessionStorage.removeItem('mobile-use.token');
    setToken('');
    setActor(undefined);
    setSystemInfo(undefined);
    setDevices([]);
    setSessions([]);
    setAudits(undefined);
    setSelected('');
    setTab('Overview');
    setInventoryState('loading');
    setAuditState('idle');
    setShowAddDevice(false);
    setError('');
    history.pushState(null, '', consolePath('Overview'));
  }

  if (!actor && (authConfigLoading || identityLoading))
    return (
      <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
        <TooltipProvider>
          <div className="grid min-h-svh place-items-center bg-muted/40 p-4">
            <Card className="w-full max-w-md">
              <CardHeader className="items-center text-center">
                <span className="grid size-12 place-items-center rounded-2xl bg-primary text-primary-foreground">
                  <Smartphone className="size-6" />
                </span>
                <CardTitle>Opening mobile-use</CardTitle>
                <CardDescription>Checking your local workspace and sign-in.</CardDescription>
              </CardHeader>
              <CardContent className="grid gap-3">
                <Skeleton className="h-10 w-full" />
                <Skeleton className="h-10 w-2/3" />
              </CardContent>
            </Card>
          </div>
        </TooltipProvider>
      </ThemeProvider>
    );

  if (!actor)
    return (
      <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
        <TooltipProvider>
          <div className="grid min-h-svh place-items-center bg-muted/40 p-4">
            <Card className="w-full max-w-md border-primary/15 shadow-lg">
              <CardHeader className="items-center text-center">
                <span className="mb-2 grid size-14 place-items-center rounded-2xl bg-primary text-primary-foreground">
                  <Smartphone className="size-7" />
                </span>
                <CardTitle className="text-2xl">mobile-use</CardTitle>
                <CardDescription>
                  Connect a phone, approve access, and work with it from your agent.
                </CardDescription>
              </CardHeader>
              <CardContent className="grid gap-4">
                {error && (
                  <Alert variant="destructive">
                    <AlertCircle />
                    <AlertTitle>Sign-in failed</AlertTitle>
                    <AlertDescription>{error}</AlertDescription>
                  </Alert>
                )}
                {oidc?.client_id ? (
                  <Button
                    className="w-full"
                    onClick={() => beginLogin(oidc).catch((error) => setError(errorText(error)))}
                  >
                    Continue to sign in <ArrowRight data-icon="inline-end" />
                  </Button>
                ) : (
                  <form
                    className="grid gap-4"
                    onSubmit={(event) => {
                      event.preventDefault();
                      setError('');
                      signedIn(draftToken.trim());
                    }}
                  >
                    <FieldGroup>
                      <Field>
                        <FieldLabel htmlFor="credential">Access token</FieldLabel>
                        <Input
                          id="credential"
                          type="password"
                          autoComplete="off"
                          required
                          value={draftToken}
                          onChange={(event) => setDraftToken(event.target.value)}
                        />
                        <FieldDescription>
                          For local setup, open the desktop console from the CLI to sign in automatically.
                        </FieldDescription>
                      </Field>
                    </FieldGroup>
                    <Button className="w-full" type="submit">
                      Open console
                    </Button>
                  </form>
                )}
                <Separator />
                <div className="grid gap-2 text-sm text-muted-foreground">
                  <p className="font-medium text-foreground">Zero-config local start</p>
                  <p>
                    Run <code className="rounded bg-muted px-1.5 py-0.5">mobile-use ui</code> on the
                    workstation. It opens this console with a short-lived launch ticket.
                  </p>
                  <p>
                    For manual sign-in, use{' '}
                    <code className="rounded bg-muted px-1.5 py-0.5">mobile-use token admin</code> and paste
                    the displayed token.
                  </p>
                </div>
              </CardContent>
            </Card>
          </div>
        </TooltipProvider>
      </ThemeProvider>
    );

  const manage = ['admin', 'operator'].includes(actor.role);
  const canAddDevice = manage || (actor.role === 'owner' && Boolean(oidc?.companion_enabled));
  const device = devices.find((item) => item.id === selected);
  const canRemoveDevice = manage || (actor.role === 'owner' && device?.owner === actor.subject);
  const deviceSessions = sessions.filter((item) => item.device_id === selected);
  const active = deviceSessions.find((item) => item.state === 'active');
  const pending = deviceSessions.find((item) => item.state === 'pending_approval');
  const change = (session: Session, verb: string) =>
    run(
      () => api(token, `/v1/sessions/${session.id}/${verb}`, 'POST'),
      ({
        approve: 'Session approved. Start screen sharing on the phone to send frames.',
        deny: 'Access request denied.',
        end: 'Session ended.',
        pause: 'Session paused.',
        resume: 'Session resumed.',
        takeover: 'Control transferred to you.',
      } as Record<string, string>)[verb],
    );
  const setPage = (page: ConsolePage) => {
    setTab(page);
    setShowAddDevice(false);
    history.pushState(null, '', consolePath(page, { deviceId: selected, auditPage, auditPageSize }));
  };
  const selectDevice = (id: string) => {
    setSelected(id);
    history.pushState(null, '', consolePath('Devices', { deviceId: id }));
  };
  const selectAuditPage = (page: number, pageSize = auditPageSize) => {
    const nextPage = Math.max(1, page);
    setAuditPage(nextPage);
    history.pushState(
      null,
      '',
      consolePath('Audit', { auditPage: nextPage, auditPageSize: pageSize }),
    );
  };
  const inventoryLoading = inventoryState === 'loading' && devices.length === 0 && sessions.length === 0;
  const titleByPage: Record<Tab, string> = {
    Overview: 'Overview',
    Devices: 'Devices',
    Sessions: 'Sessions',
    'Agent setup': 'Agent setup',
    Downloads: 'Downloads',
    Audit: 'Audit log',
  };
  const pendingCount = sessions.filter((session) => session.state === 'pending_approval').length;

  return (
    <ThemeProvider attribute="class" defaultTheme="system" enableSystem>
      <TooltipProvider>
        <SidebarProvider>
          <AppSidebar
            page={tab}
            setPage={setPage}
            actor={actor}
            version={systemInfo?.version}
            sessionsCount={pendingCount}
            onSignOut={signOut}
          />
          <SidebarInset>
            <header className="sticky top-0 z-20 flex h-16 shrink-0 items-center gap-3 border-b bg-background/90 px-4 backdrop-blur sm:px-6">
              <SidebarTrigger aria-label="Toggle navigation" />
              <Separator orientation="vertical" className="h-5" />
              <Breadcrumb>
                <BreadcrumbList>
                  <BreadcrumbItem>
                    <BreadcrumbPage>{titleByPage[tab]}</BreadcrumbPage>
                  </BreadcrumbItem>
                </BreadcrumbList>
              </Breadcrumb>
              <div className="ml-auto flex items-center gap-2">
                {lastRefreshedAt && (
                  <span className="hidden text-xs text-muted-foreground md:inline" aria-live="polite">
                    Updated {lastRefreshedAt.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })}
                  </span>
                )}
                <Badge variant="outline" className="hidden sm:inline-flex">
                  {actor.workspace === 'local' ? 'Local' : actor.workspace}
                </Badge>
                <ThemeToggle />
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={() => void refreshWorkspace()}
                >
                  <RefreshCw data-icon="inline-start" className={busy ? 'animate-spin' : ''} />
                  Refresh
                </Button>
              </div>
            </header>
            <main className="mx-auto grid w-full max-w-[1440px] gap-6 p-4 sm:p-6 lg:p-8">
              <div className="flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
                <div className="grid gap-1">
                  <h1 className="text-3xl font-semibold tracking-tight">{titleByPage[tab]}</h1>
                  <p className="text-sm text-muted-foreground">
                    {tab === 'Overview'
                      ? 'Your connected devices, session activity, and setup progress.'
                      : tab === 'Devices'
                        ? 'Pair phones, manage access, and control approved sessions.'
                        : tab === 'Sessions'
                          ? 'Review requests and monitor active device access.'
                          : tab === 'Agent setup'
                            ? 'Configure MCP clients and per-device agent grants.'
                            : tab === 'Downloads'
                              ? 'Get the app, workstation tools, and guided setup.'
                              : 'Review access and operation history.'}
                  </p>
                </div>
                {tab === 'Devices' && canAddDevice && (
                  <Button onClick={() => setShowAddDevice(true)}>
                    <Smartphone data-icon="inline-start" />
                    Add device
                  </Button>
                )}
              </div>
              {error && (
                <Alert variant="destructive">
                  <AlertCircle />
                  <AlertTitle>Action needs attention</AlertTitle>
                  <AlertDescription className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                    <span>{error}</span>
                    <div className="flex shrink-0 gap-2">
                      {inventoryState === 'error' && (
                        <Button size="sm" variant="outline" disabled={busy} onClick={() => void run(() => refresh(token))}>
                          Retry
                        </Button>
                      )}
                      <Button size="sm" variant="outline" onClick={() => setError('')}>
                        Dismiss
                      </Button>
                    </div>
                  </AlertDescription>
                </Alert>
              )}
              {tab === 'Overview' && (inventoryLoading ? <PageLoading /> : (
                inventoryState === 'error' && devices.length === 0
                  ? <LoadError retry={() => void run(() => refresh(token))} />
                  : <Overview
                      devices={devices}
                      sessions={sessions}
                      systemInfo={systemInfo}
                      onNavigate={setPage}
                      onAddDevice={() => setShowAddDevice(true)}
                    />
              ))}
              {tab === 'Devices' && inventoryLoading && <PageLoading />}
              {tab === 'Devices' && inventoryState === 'error' && devices.length === 0 && (
                <LoadError retry={() => void run(() => refresh(token))} />
              )}
              {tab === 'Devices' && !inventoryLoading && !(inventoryState === 'error' && devices.length === 0) && (
                <div className="grid gap-4 xl:grid-cols-[minmax(17rem,0.72fr)_minmax(0,1.55fr)]">
                  <Card className="min-w-0">
                    <CardHeader className="grid-cols-[minmax(0,1fr)_auto] items-center justify-between gap-2">
                      <div className="grid gap-1">
                        <CardTitle>Device inventory</CardTitle>
                        <CardDescription>
                          {devices.length} {devices.length === 1 ? 'phone' : 'phones'}
                        </CardDescription>
                      </div>
                      <Smartphone className="size-5 text-muted-foreground" />
                    </CardHeader>
                    <CardContent className="p-0">
                      <Separator />
                      {devices.length === 0 ? (
                        <Empty className="min-h-64 border-0">
                          <EmptyHeader>
                            <EmptyMedia variant="icon">
                              <Smartphone />
                            </EmptyMedia>
                            <EmptyTitle>Connect your first phone</EmptyTitle>
                            <EmptyDescription>
                              Pair the companion for screen sharing, or use ADB for input and app controls.
                            </EmptyDescription>
                          </EmptyHeader>
                          <EmptyContent>
                            <Button onClick={() => setShowAddDevice(true)}>Add device</Button>
                          </EmptyContent>
                        </Empty>
                      ) : (
                        <ScrollArea className="max-h-[70vh]">
                          <div className="grid gap-1 p-2">
                            {devices.map((item) => (
                              <Button
                                type="button"
                                variant="ghost"
                                key={item.id}
                                onClick={() => selectDevice(item.id)}
                                aria-pressed={item.id === selected}
                                className={`h-auto w-full justify-start gap-3 rounded-lg border p-3 text-left whitespace-normal ${item.id === selected ? 'border-primary/40 bg-primary/5' : 'border-transparent'}`}
                              >
                                <span className="grid size-10 shrink-0 place-items-center rounded-xl bg-secondary text-secondary-foreground">
                                  <Smartphone />
                                </span>
                                <span className="grid min-w-0 flex-1 gap-1 text-left">
                                  <strong className="truncate text-sm">{item.label}</strong>
                                  <span className="truncate text-xs text-muted-foreground">
                                    {item.platform === 'ios' ? 'iOS' : 'Android'} ·{' '}
                                    {item.virtual ? 'Virtual device' : item.profile || 'Awaiting enrollment'}
                                  </span>
                                </span>
                                <Badge className="ml-auto" variant={item.online ? 'secondary' : 'outline'}>
                                  {item.online ? 'Online' : 'Offline'}
                                </Badge>
                              </Button>
                            ))}
                          </div>
                        </ScrollArea>
                      )}
                    </CardContent>
                  </Card>
                  {device ? (
                    <div className="grid min-w-0 gap-4">
                      <Card>
                        <CardHeader className="grid-cols-[minmax(0,1fr)_auto] items-start justify-between gap-4">
                          <div className="grid gap-1">
                            <CardTitle>{device.label}</CardTitle>
                            <CardDescription>
                              {device.platform === 'ios' ? 'iOS' : 'Android'} ·{' '}
                              {device.connector || device.mode}
                              {device.virtual ? ' · virtual' : ''}
                            </CardDescription>
                          </div>
                          <div className="flex flex-wrap items-center justify-end gap-2">
                            <Badge variant={device.online ? 'secondary' : 'outline'}>
                              {device.online ? 'Online' : 'Offline'}
                            </Badge>
                            {canRemoveDevice && (
                              <Button
                                type="button"
                                size="sm"
                                variant="destructive"
                                disabled={busy}
                                onClick={() => setRemoveTarget(device)}
                              >
                                <Trash2 data-icon="inline-start" />
                                Remove
                              </Button>
                            )}
                          </div>
                        </CardHeader>
                        <CardContent className="grid gap-4">
                          <div className="flex flex-wrap gap-2">
                            {Object.keys(device.capabilities)
                              .filter((cap) => device.capabilities[cap])
                              .map((cap) => (
                                <Badge key={cap} variant="outline">
                                  {labels[cap] || cap}
                                </Badge>
                              ))}
                          </div>
                          <Separator />
                          {device.mode === 'companion' && device.platform === 'android' && canRemoveDevice && (
                            <GestureTargetSettings
                              key={`gesture-target-${device.id}`}
                              device={device}
                              devices={devices}
                              token={token}
                              onError={setError}
                            />
                          )}
                          {deviceSessions.length > 0 && (
                            <SessionList
                              sessions={deviceSessions}
                              devices={devices}
                              actor={actor}
                              busy={busy}
                              change={change}
                              selectedDevice={device.id}
                            />
                          )}
                          {active && (
                            <Viewer
                              key={`${active.id}:${active.controller}`}
                              token={token}
                              actor={actor}
                              session={active}
                              supportsVideo={device.live_video}
                              supportsNavKeys={device.platform === 'android' && device.mode === 'adb'}
                              supportsFrameStream={device.mode === 'companion'}
                              captureRevision={captureRevision}
                              onError={setError}
                            />
                          )}
                          {manage &&
                            active?.controller === actor.subject &&
                            device.profile === 'dedicated' &&
                            !!device.catalog?.length &&
                            active.capabilities.includes('apps') &&
                            active.capabilities.includes('touch') &&
                            active.capabilities.includes('app_management') &&
                            !active.paused && (
                              <MaintenanceForm
                                key={active.id}
                                device={device}
                                token={token}
                                session={active}
                                onAttempt={() => setCaptureRevision((value) => value + 1)}
                              />
                            )}
                          {pending && (
                            <Alert>
                              <Check />
                              <AlertTitle>Access request sent · waiting for approval</AlertTitle>
                              <AlertDescription>
                                {device.mode === 'companion'
                                  ? device.online
                                    ? pending.capabilities.includes('screen')
                                      ? 'The request is waiting on the phone. After approval, start screen sharing in the companion and accept Android’s capture prompt before frames appear here.'
                                      : pending.capabilities.some((capability) => ['ui_tree', 'touch', 'text', 'browser'].includes(capability))
                                        ? 'The request is waiting on the phone. UI controls need mobile-use accessibility enabled in Android Settings and an unlocked phone; they do not need screen sharing.'
                                        : 'The request is waiting on the phone. App discovery and launch do not need screen sharing.'
                                    : 'The request is saved on the server and will reach the phone when the companion reconnects.'
                                  : 'The request is waiting for owner approval. After approval, the workstation connector can capture the device screen.'}
                                <div className="mt-2 grid gap-1 text-xs">
                                  <span>
                                    <span className="font-medium">Purpose:</span> {pending.purpose}
                                  </span>
                                  <span>
                                    <span className="font-medium">Requested:</span>{' '}
                                    {pending.capabilities.map((capability) => labels[capability] || capability).join(', ')}
                                  </span>
                                  <span>
                                    <span className="font-medium">Request ID:</span>{' '}
                                    <code className="break-all">{pending.id}</code>
                                  </span>
                                </div>
                              </AlertDescription>
                            </Alert>
                          )}
                          {device.enabled && !device.online && !active && !pending && actor.role !== 'auditor' && (
                            <Alert>
                              <AlertCircle />
                              <AlertTitle>Device is offline</AlertTitle>
                              <AlertDescription>
                                {device.mode === 'companion'
                                  ? 'The companion is not checking for requests right now. Open mobile-use on Android, or enable Background connection and allow notifications to receive approvals while the app is closed.'
                                  : 'Reconnect the device before requesting access. Its current connection is not available to this workspace.'}
                              </AlertDescription>
                            </Alert>
                          )}
                          {device.enabled && device.online && !active && !pending && actor.role !== 'auditor' && (
                            <StartForm
                              key={`start-${device.id}`}
                              device={device}
                              busy={busy}
                              submit={(body) =>
                                run(() => api<Session>(token, '/v1/sessions', 'POST', body))
                              }
                            />
                          )}
                          {manage && (
                            <EnrollmentForm
                              key={device.id}
                              actor={actor}
                              device={device}
                              busy={busy}
                              submit={(body) =>
                                run(
                                  () => api(token, `/v1/devices/${device.id}`, 'PUT', body),
                                  'Device access settings saved.',
                                )
                              }
                              onUnpair={
                                device.mode === 'companion'
                                  ? () => run(
                                      () => api(token, `/v1/companion/${device.id}`, 'DELETE'),
                                      'Companion unpaired.',
                                    )
                                  : undefined
                              }
                            />
                          )}
                        </CardContent>
                      </Card>
                    </div>
                  ) : (
                    <Card>
                      <Empty className="min-h-96 border-0">
                        <EmptyHeader>
                          <EmptyMedia variant="icon">
                            <CircleHelp />
                          </EmptyMedia>
                          <EmptyTitle>Select a device</EmptyTitle>
                          <EmptyDescription>
                            Choose a phone from the inventory to see its sessions, screen, and access
                            settings.
                          </EmptyDescription>
                        </EmptyHeader>
                      </Empty>
                    </Card>
                  )}
                </div>
              )}
              {tab === 'Sessions' && inventoryLoading && <PageLoading />}
              {tab === 'Sessions' && inventoryState === 'error' && sessions.length === 0 && devices.length === 0 && (
                <LoadError retry={() => void run(() => refresh(token))} />
              )}
              {tab === 'Sessions' && !inventoryLoading && !(inventoryState === 'error' && sessions.length === 0 && devices.length === 0) && (
                <div className="grid gap-4 xl:grid-cols-[minmax(18rem,0.72fr)_minmax(0,1.3fr)]">
                  <Card>
                    <CardHeader>
                      <CardTitle>Session requests</CardTitle>
                      <CardDescription>Pending approvals and current access.</CardDescription>
                    </CardHeader>
                    <CardContent>
                      <SessionList
                        sessions={sessions}
                        devices={devices}
                        actor={actor}
                        busy={busy}
                        change={change}
                      />
                    </CardContent>
                  </Card>
                  <div className="grid min-w-0 gap-4">
                    {sessions
                      .filter((item) => item.state === 'active')
                      .map((item) => (
                        <Viewer
                          key={item.id}
                          token={token}
                          actor={actor}
                          session={item}
                          supportsVideo={devices.some(
                            (entry) => entry.id === item.device_id && entry.live_video,
                          )}
                          supportsNavKeys={devices.some(
                            (entry) => entry.id === item.device_id && entry.mode !== 'companion',
                          )}
                          supportsFrameStream={devices.some(
                            (entry) => entry.id === item.device_id && entry.mode === 'companion',
                          )}
                          onError={setError}
                        />
                      ))}
                    {sessions.every((item) => item.state !== 'active') && (
                      <Card>
                        <Empty className="min-h-64 border-0">
                          <EmptyHeader>
                            <EmptyMedia variant="icon">
                              <Activity />
                            </EmptyMedia>
                            <EmptyTitle>No active sessions</EmptyTitle>
                            <EmptyDescription>
                              Approved sessions and their live screen will appear here.
                            </EmptyDescription>
                          </EmptyHeader>
                        </Empty>
                      </Card>
                    )}
                  </div>
                </div>
              )}
              {tab === 'Agent setup' && (
                <Suspense fallback={<PageLoading />}>
                  <McpSetup
                    devices={devices}
                    systemInfo={systemInfo}
                    onSelectDevice={(id) => {
                      selectDevice(id);
                      setTab('Devices');
                    }}
                    onNavigateDownloads={() => setPage('Downloads')}
                    onAddDevice={() => setShowAddDevice(true)}
                  />
                </Suspense>
              )}
              {tab === 'Downloads' && (
                <Suspense fallback={<PageLoading />}>
                  <Downloads systemInfo={systemInfo} />
                </Suspense>
              )}
              {tab === 'Audit' && (
                <Card>
                  <CardHeader className="gap-4 sm:flex-row sm:items-start sm:justify-between">
                    <div className="grid gap-1">
                      <CardTitle>Workspace audit</CardTitle>
                      <CardDescription>Access decisions and device operations recorded for this workspace.</CardDescription>
                    </div>
                    <Button type="button" size="sm" variant="outline" disabled={auditState === 'loading'} onClick={() => setAuditReload((value) => value + 1)}>
                      <RefreshCw data-icon="inline-start" className={auditState === 'loading' ? 'animate-spin' : ''} />
                      Refresh audit
                    </Button>
                  </CardHeader>
                  <CardContent className="grid gap-4">
                    <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                      <p className="text-sm text-muted-foreground" aria-live="polite">
                        {audits ? `${audits.total.toLocaleString()} total events` : 'Workspace events'}
                      </p>
                      <Field orientation="horizontal" className="w-fit items-center gap-2">
                        <FieldLabel htmlFor="audit-page-size" className="whitespace-nowrap text-sm font-normal text-muted-foreground">Rows per page</FieldLabel>
                        <Select value={String(auditPageSize)} onValueChange={(value) => {
                          const size = Number(value);
                          if (![10, 25, 50, 100].includes(size)) return;
                          setAuditPageSize(size);
                          selectAuditPage(1, size);
                        }}>
                          <SelectTrigger id="audit-page-size" className="w-24" aria-label="Audit rows per page"><SelectValue /></SelectTrigger>
                          <SelectContent><SelectGroup>{[10, 25, 50, 100].map((size) => <SelectItem key={size} value={String(size)}>{size}</SelectItem>)}</SelectGroup></SelectContent>
                        </Select>
                      </Field>
                    </div>
                    {auditState === 'loading' && (
                      <div className="grid gap-2 rounded-md border p-4" role="status" aria-label="Loading audit events">
                        {[0, 1, 2, 3, 4].map((row) => <Skeleton key={row} className="h-10 w-full" />)}
                      </div>
                    )}
                    {auditState === 'error' && (
                      <Alert variant="destructive">
                        <AlertCircle />
                        <AlertTitle>Audit events could not be loaded</AlertTitle>
                        <AlertDescription className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
                          <span>{auditError}</span>
                          <Button type="button" size="sm" variant="outline" onClick={() => setAuditReload((value) => value + 1)}>Try again</Button>
                        </AlertDescription>
                      </Alert>
                    )}
                    {auditState === 'ready' && audits?.items.length === 0 && (
                      <Empty className="rounded-md border">
                        <EmptyHeader>
                          <EmptyMedia variant="icon">
                            <ShieldCheck />
                          </EmptyMedia>
                          <EmptyTitle>No audit events yet</EmptyTitle>
                          <EmptyDescription>
                            Events will appear here as devices connect and sessions are requested.
                          </EmptyDescription>
                        </EmptyHeader>
                      </Empty>
                    )}
                    {auditState === 'ready' && Boolean(audits?.items.length) && (
                      <ScrollArea className="max-h-[68vh] rounded-md border">
                        <Table className="min-w-[58rem]">
                          <TableHeader className="sticky top-0 z-10 bg-card">
                            <TableRow>
                              <TableHead>Time</TableHead>
                              <TableHead>Event</TableHead>
                              <TableHead>Identity</TableHead>
                              <TableHead>Device</TableHead>
                              <TableHead>Session</TableHead>
                              <TableHead>Result</TableHead>
                            </TableRow>
                          </TableHeader>
                          <TableBody>
                            {audits?.items.map((item, index) => (
                              <TableRow key={`${item.at}-${item.session || item.event}-${index}`}>
                                <TableCell className="whitespace-nowrap">
                                  <time dateTime={item.at}>{new Date(item.at).toLocaleString()}</time>
                                </TableCell>
                                <TableCell className="capitalize">{item.event.replaceAll('_', ' ')}</TableCell>
                                <TableCell>{item.actor}</TableCell>
                                <TableCell title={item.device}>{item.device ? item.device.slice(0, 8) : '—'}</TableCell>
                                <TableCell title={item.session}>{item.session ? item.session.slice(0, 8) : '—'}</TableCell>
                                <TableCell>{item.code?.replaceAll('_', ' ') || '—'}</TableCell>
                              </TableRow>
                            ))}
                          </TableBody>
                        </Table>
                      </ScrollArea>
                    )}
                    {auditState === 'ready' && (audits?.total_pages || 0) > 1 && (
                      <div className="flex flex-col gap-3 border-t pt-4 sm:flex-row sm:items-center sm:justify-between">
                        <p className="text-sm text-muted-foreground" aria-live="polite">
                          Showing {((auditPage - 1) * auditPageSize + 1).toLocaleString()}–{Math.min(auditPage * auditPageSize, audits?.total || 0).toLocaleString()} of {(audits?.total || 0).toLocaleString()}
                        </p>
                        <Pagination className="mx-0 w-auto justify-start sm:justify-end">
                          <PaginationContent>
                            <PaginationItem>
                              <PaginationPrevious href={consolePath('Audit', { auditPage: Math.max(1, auditPage - 1), auditPageSize })} aria-disabled={auditPage <= 1} className={auditPage <= 1 ? 'pointer-events-none opacity-50' : ''} onClick={(event) => {
                                event.preventDefault();
                                if (auditPage > 1) selectAuditPage(auditPage - 1);
                              }} />
                            </PaginationItem>
                            {auditPageNumbers(auditPage, audits?.total_pages || 0).map((page) => (
                              <PaginationItem key={page}>
                                <PaginationLink href={consolePath('Audit', { auditPage: page, auditPageSize })} isActive={page === auditPage} aria-label={`Go to page ${page}`} onClick={(event) => {
                                  event.preventDefault();
                                  selectAuditPage(page);
                                }}>{page}</PaginationLink>
                              </PaginationItem>
                            ))}
                            <PaginationItem>
                              <PaginationNext href={consolePath('Audit', { auditPage: Math.min(audits?.total_pages || 1, auditPage + 1), auditPageSize })} aria-disabled={auditPage >= (audits?.total_pages || 1)} className={auditPage >= (audits?.total_pages || 1) ? 'pointer-events-none opacity-50' : ''} onClick={(event) => {
                                event.preventDefault();
                                if (auditPage < (audits?.total_pages || 1)) selectAuditPage(auditPage + 1);
                              }} />
                            </PaginationItem>
                          </PaginationContent>
                        </Pagination>
                      </div>
                    )}
                  </CardContent>
                </Card>
              )}
            </main>
            <AlertDialog
              open={Boolean(removeTarget)}
              onOpenChange={(open) => {
                if (!open) setRemoveTarget(undefined);
              }}
            >
              <AlertDialogContent>
                <AlertDialogHeader>
                  <AlertDialogTitle>Remove {removeTarget?.label} from inventory?</AlertDialogTitle>
                  <AlertDialogDescription>
                    Active sessions will end and access will be revoked. A companion phone will need to
                    be paired again before it can reconnect.
                  </AlertDialogDescription>
                </AlertDialogHeader>
                <AlertDialogFooter>
                  <AlertDialogCancel disabled={busy}>Keep device</AlertDialogCancel>
                  <AlertDialogAction
                    disabled={busy || !removeTarget}
                    onClick={() => {
                      const id = removeTarget?.id;
                      if (!id) return;
                      void run(async () => {
                        await api(token, `/v1/devices/${id}`, 'DELETE');
                        setSelected('');
                      });
                      setRemoveTarget(undefined);
                    }}
                  >
                    Remove device
                  </AlertDialogAction>
                </AlertDialogFooter>
              </AlertDialogContent>
            </AlertDialog>
            <Sheet open={showAddDevice} onOpenChange={setShowAddDevice}>
              <SheetContent
                side="right"
                className="h-dvh w-full max-w-none overflow-y-auto sm:w-[min(48rem,100vw)]"
                showCloseButton={false}
              >
                <SheetHeader className="sr-only">
                  <SheetTitle>Add a device</SheetTitle>
                  <SheetDescription>
                    Choose companion pairing, Wi-Fi ADB, or remote connection.
                  </SheetDescription>
                </SheetHeader>
                <div className="min-w-0 pb-6">
                  <AddDevicePanel
                    token={token}
                    actor={actor}
                    systemInfo={systemInfo}
                    companionEnabled={Boolean(oidc?.companion_enabled)}
                    onClose={() => setShowAddDevice(false)}
                    onNavigateDownloads={() => setPage('Downloads')}
                    onDeviceAdded={async (deviceId) => {
                      await refresh(token);
                      if (deviceId) {
                        selectDevice(deviceId);
                        setTab('Devices');
                        setShowAddDevice(false);
                      }
                    }}
                  />
                </div>
              </SheetContent>
            </Sheet>
          </SidebarInset>
          <Toaster richColors position="bottom-right" />
        </SidebarProvider>
      </TooltipProvider>
    </ThemeProvider>
  );
}

function ThemeToggle() {
  const { resolvedTheme, setTheme } = useTheme();
  const dark = resolvedTheme === 'dark';
  return (
    <Button
      variant="ghost"
      size="icon"
      aria-label={`Switch to ${dark ? 'light' : 'dark'} theme`}
      title={`Switch to ${dark ? 'light' : 'dark'} theme`}
      onClick={() => setTheme(dark ? 'light' : 'dark')}
    >
      {dark ? <Sun /> : <Moon />}
    </Button>
  );
}

function PageLoading() {
  return (
    <div className="grid gap-4">
      <Skeleton className="h-36 w-full rounded-xl" />
      <div className="grid gap-4 sm:grid-cols-2">
        <Skeleton className="h-52 rounded-xl" />
        <Skeleton className="h-52 rounded-xl" />
      </div>
      <Skeleton className="h-72 w-full rounded-xl" />
    </div>
  );
}

function LoadError({ retry }: { retry: () => void }) {
  return (
    <Card>
      <Empty className="min-h-72 border-0">
        <EmptyHeader>
          <EmptyMedia variant="icon"><AlertCircle /></EmptyMedia>
          <EmptyTitle>Workspace data could not be loaded</EmptyTitle>
          <EmptyDescription>Check the connection to the mobile-use service, then try again.</EmptyDescription>
        </EmptyHeader>
        <EmptyContent><Button onClick={retry}>Try again</Button></EmptyContent>
      </Empty>
    </Card>
  );
}

function SessionList({
  sessions,
  devices,
  actor,
  busy,
  change,
  selectedDevice,
}: {
  sessions: Session[];
  devices: Device[];
  actor: Actor;
  busy: boolean;
  change: (item: Session, verb: string) => void;
  selectedDevice?: string;
}) {
  if (sessions.length === 0)
    return (
      <Empty>
        <EmptyHeader>
          <EmptyMedia variant="icon">
            <Activity />
          </EmptyMedia>
          <EmptyTitle>No session activity</EmptyTitle>
          <EmptyDescription>Requests and active device access will show up here.</EmptyDescription>
        </EmptyHeader>
      </Empty>
    );
  return (
    <div className="grid gap-3">
      {sessions.map((item) => {
        const state = item.paused
          ? 'Paused'
          : item.state === 'active'
            ? 'Active'
            : item.state === 'pending_approval'
              ? 'Approval needed'
              : item.state.replaceAll('_', ' ');
        const isActive = item.state === 'active' && !item.paused;
        return (
          <Card
            key={item.id}
            className={item.state === 'pending_approval' ? 'border-primary/30 bg-primary/[0.025]' : ''}
          >
            <CardContent className="grid gap-3 p-4">
              <div className="flex flex-wrap items-start justify-between gap-2">
                <div className="grid gap-1">
                  <p className="font-medium leading-snug">{item.purpose}</p>
                  <p className="text-sm text-muted-foreground">
                    {selectedDevice
                      ? ''
                      : `${devices.find((device) => device.id === item.device_id)?.label || 'Phone'} · `}
                    {item.requester} · expires {time(item.expires_at)}
                  </p>
                </div>
                <Badge
                  variant={isActive ? 'secondary' : item.state === 'pending_approval' ? 'default' : 'outline'}
                >
                  {state}
                </Badge>
              </div>
              <div className="flex flex-wrap gap-1.5">
                {item.capabilities.map((cap) => (
                  <Badge key={cap} variant="outline">
                    {labels[cap] || cap}
                  </Badge>
                ))}
              </div>
              {item.owner === actor.subject && item.state === 'pending_approval' && (
                <Alert>
                  <ShieldCheck />
                  <AlertTitle>Owner approval required</AlertTitle>
                  <AlertDescription>
                    Review the requested capabilities before approving this time-limited session.
                  </AlertDescription>
                </Alert>
              )}
              {item.owner === actor.subject && item.state === 'pending_approval' && (
                <div className="flex flex-wrap justify-end gap-2">
                  <Button
                    type="button"
                    variant="outline"
                    disabled={busy}
                    onClick={() => change(item, 'deny')}
                  >
                    Deny request
                  </Button>
                  <Button type="button" disabled={busy} onClick={() => change(item, 'approve')}>
                    Approve session
                  </Button>
                </div>
              )}
              {item.state === 'active' &&
                (item.owner === actor.subject ||
                  item.requester === actor.subject ||
                  ['admin', 'operator'].includes(actor.role)) && (
                  <div className="flex flex-wrap justify-end gap-2">
                    {item.owner === actor.subject && (
                      <Button
                        type="button"
                        variant="outline"
                        disabled={busy}
                        onClick={() => change(item, item.paused ? 'resume' : 'pause')}
                      >
                        {item.paused ? 'Resume' : 'Pause'}
                      </Button>
                    )}
                    {item.owner === actor.subject && item.controller !== actor.subject && (
                      <Button
                        type="button"
                        variant="outline"
                        disabled={busy}
                        onClick={() => change(item, 'takeover')}
                      >
                        Take over control
                      </Button>
                    )}
                    <Button
                      type="button"
                      variant="destructive"
                      disabled={busy}
                      onClick={() => change(item, 'end')}
                    >
                      End session
                    </Button>
                  </div>
                )}
            </CardContent>
          </Card>
        );
      })}
    </div>
  );
}

function GestureTargetSettings({
  device,
  devices,
  token,
  onError,
}: {
  device: Device;
  devices: Device[];
  token: string;
  onError: (message: string) => void;
}) {
  const [targetId, setTargetId] = useState('');
  const [choice, setChoice] = useState('none');
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const targets = devices.filter(
    (item) => item.id !== device.id && item.platform === 'android' && item.mode === 'adb' &&
      item.enabled && item.owner === device.owner,
  );

  useEffect(() => {
    let current = true;
    setLoading(true);
    api<{ target_device_id: string }>(
      token,
      `/v1/companion/gesture-target/${encodeURIComponent(device.id)}`,
    ).then((result) => {
      if (!current) return;
      const value = result.target_device_id || '';
      setTargetId(value);
      setChoice(value || 'none');
    }).catch((reason: unknown) => {
      if (current) onError(reason instanceof Error ? reason.message : 'The gesture link could not be loaded.');
    }).finally(() => {
      if (current) setLoading(false);
    });
    return () => { current = false; };
  }, [device.id, onError, token]);

  const save = async () => {
    const next = choice === 'none' ? '' : choice;
    setSaving(true);
    try {
      await api(token, `/v1/companion/gesture-target/${encodeURIComponent(device.id)}`, 'PUT', {
        target_device_id: next,
      });
      setTargetId(next);
      toast.success(next ? 'Agent gesture pointer linked to the ADB device.' : 'Agent gesture pointer unlinked.');
    } catch (reason) {
      onError(reason instanceof Error ? reason.message : 'The gesture link could not be saved.');
    } finally {
      setSaving(false);
    }
  };

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Agent gesture pointer</CardTitle>
        <CardDescription>
          Mirror agent touch gestures on this phone while screen sharing. Choose the enrolled ADB entry for this same device.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3">
        <Select value={choice} onValueChange={(value) => setChoice(value || 'none')} disabled={loading || saving}>
          <SelectTrigger aria-label="ADB device for gesture pointer" className="w-full">
            <SelectValue placeholder="Choose an ADB device" />
          </SelectTrigger>
          <SelectContent>
            <SelectGroup>
              <SelectItem value="none">No ADB device linked</SelectItem>
              {targets.map((item) => (
                <SelectItem key={item.id} value={item.id}>
                  {item.label} · {item.online ? 'online' : 'offline'}
                </SelectItem>
              ))}
            </SelectGroup>
          </SelectContent>
        </Select>
        {targets.length === 0 && (
          <p className="text-sm text-muted-foreground">
            Connect and enroll this Android phone over ADB first. The phone and ADB entry must have the same owner.
          </p>
        )}
        <p className="text-sm text-muted-foreground">
          On Android, enable <span className="font-medium text-foreground">Show agent gestures</span> and allow “display over other apps.” The pointer is visual only and does not receive taps.
          {targetId && !targets.some((item) => item.id === targetId) && ' The linked ADB entry is unavailable; choose another device or unlink it.'}
        </p>
        <div>
          <Button type="button" onClick={() => void save()} disabled={loading || saving || choice === (targetId || 'none')}>
            {saving ? 'Saving…' : 'Save gesture link'}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function EnrollmentForm({
  actor,
  device,
  busy,
  submit,
  onUnpair,
}: {
  actor: Actor;
  device: Device;
  busy: boolean;
  submit: (data: unknown) => void;
  onUnpair?: () => void;
}) {
  const [name, setName] = useState(device.label);
  const [profile, setProfile] = useState(device.profile || 'personal');
  const [owner, setOwner] = useState(device.owner || actor.subject);
  const [attemptedSave, setAttemptedSave] = useState(false);
  const [agentError, setAgentError] = useState('');
  const subjectPattern = /^[a-zA-Z0-9][a-zA-Z0-9_.:@-]{0,127}$/;
  const nameError = name.trim().length === 0
    ? 'Enter a name for this device.'
    : name.trim().length > 128
      ? 'Device names can be at most 128 characters.'
      : '';
  const ownerError = !subjectPattern.test(owner.trim())
    ? 'Use 1–128 letters, numbers, dots, underscores, colons, @ signs, or hyphens.'
    : '';
  const supported = Object.keys(device.capabilities).filter((cap) => device.capabilities[cap]);
  const [grantList, setGrantList] = useState<Grant[]>(() =>
    (device.grants || []).map((g) => ({ subject: g.subject, capabilities: [...g.capabilities] })),
  );
  const [newSubject, setNewSubject] = useState('');
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const [confirmUnpair, setConfirmUnpair] = useState(false);

  function addAgent(subject: string) {
    const clean = subject.trim();
    if (!subjectPattern.test(clean)) {
      setAgentError('Enter a valid agent identity (1–128 letters, numbers, dots, underscores, colons, @ signs, or hyphens).');
      return;
    }
    if (grantList.some((g) => g.subject === clean)) {
      setAgentError('That agent already has a grant for this device.');
      return;
    }
    setGrantList((current) => [...current, { subject: clean, capabilities: [...supported] }]);
    setNewSubject('');
    setAgentError('');
  }

  function toggleCap(subject: string, cap: string, checked: boolean) {
    setGrantList((current) =>
      current.map((g) => {
        if (g.subject !== subject) return g;
        const next = checked ? [...g.capabilities, cap] : g.capabilities.filter((c) => c !== cap);
        return { ...g, capabilities: next };
      }),
    );
  }

  function onboard(event: FormEvent) {
    event.preventDefault();
    setAttemptedSave(true);
    if (nameError || ownerError) return;
    const cleaned = grantList
      .map((g) => ({
        subject: g.subject.trim(),
        capabilities: g.capabilities.filter((c) => supported.includes(c)),
      }))
      .filter((g) => g.subject && g.capabilities.length > 0);
    submit({ label: name.trim(), owner: owner.trim(), profile, grants: cleaned, enabled: true });
  }

  const hasLocalAgent = grantList.some((g) => g.subject === 'local-agent');

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">
          {device.enabled ? 'Enrollment and agent access' : 'Enroll this device'}
        </CardTitle>
        <CardDescription>
          Set an owner and profile, then choose which agent identities may request access.
        </CardDescription>
      </CardHeader>
      <CardContent className="grid gap-5">
        <form noValidate onSubmit={onboard} className="grid gap-5">
          <FieldGroup className="sm:flex-row">
            <Field className="sm:flex-1" data-invalid={attemptedSave && Boolean(nameError)}>
              <FieldLabel htmlFor="device-name">Device name</FieldLabel>
              <Input
                id="device-name"
                maxLength={128}
                aria-invalid={attemptedSave && Boolean(nameError)}
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
              {attemptedSave && nameError && <FieldError>{nameError}</FieldError>}
            </Field>
            <Field className="sm:flex-1" data-invalid={attemptedSave && Boolean(ownerError)}>
              <FieldLabel htmlFor="device-owner">Owner identity</FieldLabel>
              <Input
                id="device-owner"
                aria-invalid={attemptedSave && Boolean(ownerError)}
                value={owner}
                onChange={(event) => setOwner(event.target.value)}
              />
              {attemptedSave && ownerError && <FieldError>{ownerError}</FieldError>}
            </Field>
            <Field className="sm:flex-1">
              <FieldLabel>Access profile</FieldLabel>
              <Select
                value={profile}
                onValueChange={(value) => {
                  if (value) setProfile(value as 'personal' | 'dedicated');
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="personal">Personal · approval required</SelectItem>
                    {device.mode !== 'companion' && (
                      <SelectItem value="dedicated">Dedicated · agent grants</SelectItem>
                    )}
                  </SelectGroup>
                </SelectContent>
              </Select>
              <FieldDescription>Dedicated profiles are required for app install and reset.</FieldDescription>
            </Field>
          </FieldGroup>
          <div className="grid gap-3 rounded-lg border p-4">
            <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
              <div className="grid gap-1">
                <h3 className="font-medium">Agent grants</h3>
                <p className="text-sm text-muted-foreground">
                  Grant only the capabilities needed for each agent.
                </p>
              </div>
              {actor.workspace === 'local' && !hasLocalAgent && (
                <Button type="button" variant="outline" onClick={() => addAgent('local-agent')}>
                  Allow local-agent
                </Button>
              )}
            </div>
            {grantList.length === 0 ? (
              <p className="text-sm text-muted-foreground">
                No agent has access yet. Add an identity to allow MCP or API sessions.
              </p>
            ) : (
              <div className="grid gap-3">
                {grantList.map((grant) => (
                  <div key={grant.subject} className="grid gap-3 rounded-md bg-muted/40 p-3">
                    <div className="flex items-center justify-between gap-2">
                      <code className="font-medium">{grant.subject}</code>
                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        onClick={() =>
                          setGrantList((current) => current.filter((item) => item.subject !== grant.subject))
                        }
                      >
                        Remove
                      </Button>
                    </div>
                    <div className="flex flex-wrap gap-x-4 gap-y-2">
                      {supported.map((cap) => (
                        <Field key={cap} orientation="horizontal" className="w-auto items-center gap-2">
                          <Checkbox
                            checked={grant.capabilities.includes(cap)}
                            onCheckedChange={(checked) => toggleCap(grant.subject, cap, checked === true)}
                          />
                          <FieldLabel className="font-normal">{labels[cap] || cap}</FieldLabel>
                        </Field>
                      ))}
                    </div>
                  </div>
                ))}
              </div>
            )}
            <div className="flex flex-col gap-2 sm:flex-row">
              <Input
                aria-label="Agent identity to grant"
                placeholder={actor.workspace === 'local' ? 'local-agent' : 'agent-identity'}
                value={newSubject}
                onChange={(event) => {
                  setNewSubject(event.target.value);
                  if (agentError) setAgentError('');
                }}
                aria-invalid={Boolean(agentError)}
                onKeyDown={(event) => {
                  if (event.key === 'Enter') {
                    event.preventDefault();
                    addAgent(newSubject);
                  }
                }}
              />
              <Button
                type="button"
                variant="outline"
                disabled={!newSubject.trim()}
                onClick={() => addAgent(newSubject)}
              >
                Add agent
              </Button>
            </div>
            {agentError && <FieldError>{agentError}</FieldError>}
          </div>
          <div className="flex flex-wrap gap-2">
            <Button type="submit" disabled={busy || Boolean(nameError) || Boolean(ownerError)}>
              {busy ? 'Saving…' : device.enabled ? 'Save access' : 'Enroll device'}
            </Button>
            {device.enabled && (
              <Button
                type="button"
                variant="destructive"
                disabled={busy}
                onClick={() => setConfirmRevoke(true)}
              >
                Revoke enrollment
              </Button>
            )}
            {onUnpair && (
              <Button type="button" variant="outline" disabled={busy} onClick={() => setConfirmUnpair(true)}>
                Unpair companion
              </Button>
            )}
          </div>
        </form>
        <AlertDialog open={confirmRevoke} onOpenChange={setConfirmRevoke}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Revoke device enrollment?</AlertDialogTitle>
              <AlertDialogDescription>
                Active sessions will end and all agent grants will be removed from {device.label}.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Keep enrollment</AlertDialogCancel>
              <AlertDialogAction
                onClick={() =>
                  submit({
                    label: device.label,
                    owner: device.owner,
                    profile: device.profile,
                    grants: device.grants,
                    enabled: false,
                  })
                }
              >
                Revoke enrollment
              </AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
        <AlertDialog open={confirmUnpair} onOpenChange={setConfirmUnpair}>
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>Unpair this companion?</AlertDialogTitle>
              <AlertDialogDescription>
                This disconnects the Android app and revokes its pairing token. The phone will need a new
                pairing code.
              </AlertDialogDescription>
            </AlertDialogHeader>
            <AlertDialogFooter>
              <AlertDialogCancel>Keep paired</AlertDialogCancel>
              <AlertDialogAction onClick={() => onUnpair?.()}>Unpair companion</AlertDialogAction>
            </AlertDialogFooter>
          </AlertDialogContent>
        </AlertDialog>
      </CardContent>
    </Card>
  );
}

function StartForm({
  device,
  busy,
  submit,
}: {
  device: Device;
  busy: boolean;
  submit: (value: unknown) => Promise<Session | undefined>;
}) {
  const [purpose, setPurpose] = useState('');
  const [attempted, setAttempted] = useState(false);
  const [submitted, setSubmitted] = useState<Session>();
  const submitInFlight = useRef(false);
  const purposeValue = purpose.trim();
  const purposeError = purposeValue.length < 3
    ? 'Enter a purpose with at least 3 non-space characters.'
    : purposeValue.length > 256
      ? 'Keep the purpose to 256 characters or fewer.'
      : '';
  const options = Object.keys(device.capabilities).filter((cap) => device.capabilities[cap]);
  const [selected, setSelected] = useState(
    options.includes('apps') ? ['apps'] : options.includes('screen') ? ['screen'] : [],
  );
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Request a device session</CardTitle>
        <CardDescription>
          The phone owner reviews every request. Screen sharing and Android’s capture prompt are needed only when you request Screen.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form
          noValidate
          className="grid gap-5"
          onSubmit={async (event) => {
            event.preventDefault();
            setAttempted(true);
            if (purposeError || selected.length === 0 || busy || submitInFlight.current) return;
            submitInFlight.current = true;
            try {
              const created = await submit({ device_id: device.id, purpose: purposeValue, capabilities: selected });
              if (created?.state === 'pending_approval') setSubmitted(created);
            } finally {
              submitInFlight.current = false;
            }
          }}
        >
          {submitted && (
            <Alert>
              <Check />
              <AlertTitle>Request created and sent to the phone</AlertTitle>
              <AlertDescription>
                Waiting for owner approval. Request ID: <code className="break-all">{submitted.id}</code>
              </AlertDescription>
            </Alert>
          )}
          <Field data-invalid={attempted && Boolean(purposeError)}>
            <FieldLabel htmlFor="session-purpose">Purpose</FieldLabel>
            <Textarea
              id="session-purpose"
              maxLength={256}
              aria-invalid={attempted && Boolean(purposeError)}
              rows={2}
              placeholder="What do you need to do on this phone?"
              value={purpose}
              onChange={(event) => setPurpose(event.target.value)}
            />
            {attempted && purposeError ? (
              <FieldError>{purposeError}</FieldError>
            ) : (
              <FieldDescription>Use 3–256 characters and avoid sharing sensitive details.</FieldDescription>
            )}
          </Field>
          <fieldset className="grid gap-3" aria-invalid={attempted && selected.length === 0}>
            <legend className="text-sm font-medium">Requested capabilities</legend>
            <p className="text-sm text-muted-foreground">Select the smallest set needed for this task.</p>
            {device.mode === 'companion' && device.platform === 'android' && (
              <p className="text-sm text-muted-foreground">
                Apps can be listed and opened after approval. Enable mobile-use under Android Settings → Accessibility for UI tree, touch, text, navigation, and browser controls; keep the phone unlocked while using them.
              </p>
            )}
            {options.length > 0 ? (
              <div className="grid gap-2 sm:grid-cols-2">
              {options.map((cap) => (
                <Field key={cap} orientation="horizontal" className="items-center rounded-lg border p-3">
                  <Checkbox
                    checked={selected.includes(cap)}
                    onCheckedChange={(checked) =>
                      setSelected((current) =>
                        checked === true ? [...current, cap] : current.filter((item) => item !== cap),
                      )
                    }
                  />
                  <FieldLabel className="font-normal">{labels[cap] || cap}</FieldLabel>
                </Field>
              ))}
              </div>
            ) : (
              <Alert variant="destructive">
                <AlertCircle />
                <AlertTitle>No session capabilities are available</AlertTitle>
                <AlertDescription>Review device enrollment and reconnect the phone before requesting access.</AlertDescription>
              </Alert>
            )}
            {attempted && selected.length === 0 && options.length > 0 && (
              <FieldError>Select at least one capability.</FieldError>
            )}
          </fieldset>
          <div>
            <Button type="submit" disabled={busy || options.length === 0}>
              {busy ? 'Sending request…' : 'Request access'}
              {!busy && <ArrowRight data-icon="inline-end" />}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  );
}

function MaintenanceForm({
  device,
  token,
  session,
  onAttempt,
}: {
  device: Device;
  token: string;
  session: Session;
  onAttempt: () => void;
}) {
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
      const result = await api<{ state: string; error?: { message: string } }>(
        token,
        `/v1/sessions/${session.id}/operations`,
        'POST',
        {
          id: crypto.randomUUID(),
          kind: verb === 'install' ? 'app_install' : 'app_reset',
          maintenance: { artifact_id: artifact, confirm },
        },
      );
      if (result.state !== 'succeeded')
        throw new Error(result.error?.message || 'The phone did not confirm the operation.');
      setNotice(verb === 'install' ? 'The APK was installed.' : 'The app data was cleared.');
    } catch (error) {
      setError(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Approved app maintenance</CardTitle>
        <CardDescription>
          Install a catalog APK or reset its app data on this dedicated device.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form className="grid gap-4" onSubmit={apply}>
          <FieldGroup className="sm:flex-row">
            <Field className="sm:flex-1">
              <FieldLabel>Approved app</FieldLabel>
              <Select
                disabled={busy}
                value={artifact}
                onValueChange={(value) => {
                  if (!value) return;
                  setArtifact(value);
                  setConfirm('');
                  setNotice('');
                  setError('');
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    {items.map((item) => (
                      <SelectItem value={item.id} key={item.id}>
                        {item.label}
                      </SelectItem>
                    ))}
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
            <Field className="sm:flex-1">
              <FieldLabel>Operation</FieldLabel>
              <Select
                disabled={busy}
                value={verb}
                onValueChange={(value) => {
                  if (!value) return;
                  setVerb(value as 'install' | 'reset');
                  setConfirm('');
                  setNotice('');
                  setError('');
                }}
              >
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectGroup>
                    <SelectItem value="install">Install APK</SelectItem>
                    <SelectItem value="reset">Reset app data</SelectItem>
                  </SelectGroup>
                </SelectContent>
              </Select>
            </Field>
          </FieldGroup>
          {verb === 'reset' && (
            <Alert>
              <AlertCircle />
              <AlertTitle>App data will be cleared</AlertTitle>
              <AlertDescription>This removes the selected app’s local data from the device.</AlertDescription>
            </Alert>
          )}
          <Field>
            <FieldLabel>
              Type <code>{phrase}</code> to confirm
            </FieldLabel>
            <Input
              autoComplete="off"
              autoCapitalize="none"
              spellCheck={false}
              disabled={busy}
              value={confirm}
              onChange={(event) => setConfirm(event.target.value)}
            />
          </Field>
          <div>
            <Button
              type="submit"
              variant={verb === 'reset' ? 'destructive' : 'default'}
              disabled={busy || confirm !== phrase}
            >
              {busy ? 'Working…' : verb === 'install' ? 'Install APK' : 'Reset app data'}
            </Button>
          </div>
          {notice && (
            <Alert>
              <Check />
              <AlertTitle>Operation completed</AlertTitle>
              <AlertDescription>{notice}</AlertDescription>
            </Alert>
          )}
          {error && (
            <Alert variant="destructive">
              <AlertTitle>Operation failed</AlertTitle>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </form>
      </CardContent>
    </Card>
  );
}

function Viewer({
  token,
  actor,
  session,
  supportsVideo,
  supportsNavKeys,
  supportsFrameStream,
  captureRevision,
  onError,
}: {
  token: string;
  actor: Actor;
  session: Session;
  supportsVideo: boolean;
  supportsNavKeys?: boolean;
  supportsFrameStream?: boolean;
  captureRevision?: number;
  onError: (message: string) => void;
}) {
  const [frame, setFrame] = useState<Observation>();
  const [treeObservation, setTreeObservation] = useState<Observation>();
  const [treeOpen, setTreeOpen] = useState(false);
  const [treeQuery, setTreeQuery] = useState('');
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState(Boolean(supportsFrameStream));
  const [streamState, setStreamState] = useState<
    'idle' | 'connecting' | 'waiting' | 'streaming' | 'reconnecting' | 'error'
  >('idle');
  const [streamMessage, setStreamMessage] = useState('');
  const [live, setLive] = useState(false);
  const [pageVisible, setPageVisible] = useState(() => !document.hidden);
  const [touchMode, setTouchMode] = useState<TouchMode>('direct');
  const [hardwareKey, setHardwareKey] = useState<AndroidKey>('enter');
  const pointers = useRef(new Map<number, ScreenPointer>());
  const base = `/v1/sessions/${session.id}`;
  const owner = session.owner === actor.subject && actor.role !== 'agent';
  const controller =
    !session.paused && session.controller === actor.subject && session.capabilities.includes('touch');
  const canView = owner || session.requester === actor.subject || controller;
  const showScreen = canView && session.capabilities.includes('screen');
  const showCamera = Boolean(canView && supportsFrameStream && session.capabilities.includes('camera'));
  const showMicrophone = Boolean(canView && supportsFrameStream && session.capabilities.includes('microphone'));
  const watchSensors = showCamera || showMicrophone;
  const watchScreen = showScreen && !controller && (preview || watchSensors);
  const companionMode = supportsFrameStream && supportsVideo && !session.paused && pageVisible;
  const { streams, failure: liveMediaFailure } = useLiveMedia(token, `${base}/video`,
    !companionMode ? 'off' : watchScreen ? (watchSensors ? 'all' : 'screen') : watchSensors ? 'sensors' : 'off', true);
  const liveScreen = companionMode && watchScreen && preview ? streams.screen : undefined;
  const treeNodes = treeObservation?.nodes ?? [];
  const treeNodeById = new Map(treeNodes.map((node) => [node.id, node]));
  const normalizedTreeQuery = treeQuery.trim().toLocaleLowerCase();
  const matchingTreeNodes = treeNodes.filter((node) =>
    !normalizedTreeQuery || [node.label, node.hint, node.role, node.resource_id]
      .some((value) => value?.toLocaleLowerCase().includes(normalizedTreeQuery)),
  );
  const shownTreeNodes = matchingTreeNodes.slice(0, 200);
  function treeDepth(node: (typeof treeNodes)[number]) {
    let depth = 0;
    let parentId = node.parent_id;
    while (parentId && depth < 8) {
      const parent = treeNodeById.get(parentId);
      if (!parent) break;
      depth += 1;
      parentId = parent.parent_id;
    }
    return depth;
  }

  const observe = useCallback(async (includeTree = false) => {
    for (let attempt = 0; ; attempt += 1) {
      const result = await api<{ observation?: Observation; error?: { code: string; message: string } }>(
        token,
        `${base}/operations`,
        'POST',
        { kind: 'observe', observe: { screen: true, tree: includeTree } },
      );
      if (!result.error) return result.observation;
      if (
        attempt >= 2 ||
        !['DEVICE_TRANSITION', 'STALE_REFERENCE', 'OPERATION_IN_PROGRESS'].includes(result.error.code)
      ) {
        throw new Error(result.error.message);
      }
      await new Promise((resolve) => setTimeout(resolve, 300));
    }
  }, [token, base]);

  async function inspectTree() {
    if (busy) return;
    setBusy(true);
    onError('');
    try {
      const next = await observe(true);
      setFrame(next);
      setTreeObservation(next);
      setTreeOpen(true);
    } catch (error) {
      onError(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    const updateVisibility = () => setPageVisible(!document.hidden);
    document.addEventListener('visibilitychange', updateVisibility);
    return () => document.removeEventListener('visibilitychange', updateVisibility);
  }, []);

  useEffect(() => {
    if (!supportsFrameStream || !showScreen || !preview || !pageVisible || session.paused || liveScreen) {
      setStreamState('idle');
      setStreamMessage('');
      return;
    }
    let closed = false;
    let connection: AbortController | undefined;
    let retryDelay = 500;
    const connect = async () => {
      while (!closed) {
        const current = new AbortController();
        connection = current;
        try {
          setStreamMessage('');
          setStreamState((current) => (current === 'streaming' ? current : 'connecting'));
          const response = await fetch(`/v1/companion/sessions/${session.id}/frames`, {
            headers: { Authorization: `Bearer ${token}`, Accept: 'text/event-stream' },
            signal: current.signal,
            redirect: 'error',
          });
          if (!response.ok || !response.body) {
            const failure = (await response.json().catch(() => undefined)) as
              | { error?: { message?: string } }
              | undefined;
            const message = failure?.error?.message || `Screen stream unavailable (${response.status}).`;
            if ([401, 403, 404, 409].includes(response.status)) {
              setStreamMessage(message);
              setStreamState('error');
              return;
            }
            throw new Error(message);
          }
          setStreamState('waiting');
          retryDelay = 500;
          const reader = response.body.getReader();
          const decoder = new TextDecoder();
          let buffer = '';
          let lastFrameAt = 0;
          while (!closed) {
            let timeout = 0;
            const pending = reader.read();
            let next: Awaited<ReturnType<typeof reader.read>>;
            try {
              next = lastFrameAt
                ? await Promise.race([
                  pending,
                  new Promise<never>((_, reject) => {
                    const remaining = Math.max(100, 3500 - (Date.now() - lastFrameAt));
                    timeout = window.setTimeout(
                      () => reject(new Error('No new screen frames arrived. Reconnecting to the phone…')),
                      remaining,
                    );
                  }),
                  ])
                : await Promise.race([
                  pending,
                  new Promise<never>((_, reject) => {
                    timeout = window.setTimeout(
                      () => reject(new Error('The screen stream is not responding. Reconnecting…')),
                      12000,
                    );
                  }),
                  ]);
            } finally {
              window.clearTimeout(timeout);
            }
            const { value, done } = next;
            if (done) break;
            buffer += decoder.decode(value, { stream: true }).replaceAll('\r\n', '\n');
            let boundary = buffer.indexOf('\n\n');
            while (boundary >= 0) {
              const block = buffer.slice(0, boundary);
              buffer = buffer.slice(boundary + 2);
              let event = 'message';
              const data: string[] = [];
              for (const line of block.split('\n')) {
                if (line.startsWith('event:')) event = line.slice(6).trim();
                else if (line.startsWith('data:')) data.push(line.slice(5).trimStart());
              }
              if (data.length) {
                if (event === 'frame') {
                  const next = JSON.parse(data.join('\n')) as Observation;
                  if (next.image) {
                    lastFrameAt = Date.now();
                    setFrame(next);
                  }
                  setStreamMessage('');
                  setStreamState('streaming');
                } else if (event === 'status') {
                  lastFrameAt = 0;
                  setStreamState('waiting');
                }
              }
              boundary = buffer.indexOf('\n\n');
            }
          }
          if (!closed) throw new Error('The companion screen stream disconnected.');
        } catch (error) {
          if (closed) return;
          setStreamMessage(errorText(error));
          setStreamState('reconnecting');
          await new Promise((resolve) => window.setTimeout(resolve, retryDelay));
          retryDelay = Math.min(3000, retryDelay * 2);
        } finally {
          if (connection === current) connection = undefined;
          current.abort();
        }
      }
    };
    void connect();
    return () => {
      closed = true;
      connection?.abort();
    };
  }, [liveScreen, pageVisible, preview, session.id, session.paused, showScreen, supportsFrameStream, token]);

  useEffect(() => {
    if (!owner || session.profile !== 'personal') return;
    const presence = () => {
      if (!document.hidden) api(token, `${base}/presence`, 'POST').catch(() => undefined);
    };
    presence();
    const timer = window.setInterval(presence, 4500);
    document.addEventListener('visibilitychange', presence);
    return () => {
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', presence);
    };
  }, [base, owner, session.profile, token]);

  useEffect(() => {
    if (supportsFrameStream || !preview || !pageVisible || busy || controller || live || session.paused)
      return;
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
        if (!closed) timer = window.setTimeout(update, 1000);
      }
    };
    void update();
    return () => {
      closed = true;
      window.clearTimeout(timer);
    };
  }, [pageVisible, preview, busy, controller, live, session.paused, supportsFrameStream, observe]);

  useEffect(() => {
    if (session.paused || !supportsVideo) setLive(false);
  }, [session.paused, supportsVideo]);

  useEffect(() => {
    pointers.current.clear();
    setFrame(undefined);
    setTreeObservation(undefined);
    setTreeOpen(false);
    setTreeQuery('');
  }, [captureRevision, session.id, session.paused]);

  async function perform(action?: Record<string, unknown>) {
    if (busy) return;
    setBusy(true);
    onError('');
    try {
      if (action && frame) {
        const result = await api<{ error?: { message: string } }>(token, `${base}/operations`, 'POST', {
          id: crypto.randomUUID(),
          kind: 'act',
          action: { ...action, frame_id: action.frame_id || frame.frame_id },
        });
        if (result.error) throw new Error(result.error.message);
      }
      const next = await observe(Boolean(treeObservation));
      setFrame(next);
      if (treeObservation) setTreeObservation(next);
    } catch (error) {
      onError(errorText(error));
    } finally {
      setBusy(false);
    }
  }

  const position = (event: PointerEvent<HTMLImageElement>) => {
    const rect = event.currentTarget.getBoundingClientRect();
    const clamp = (value: number) => Math.min(0.9999, Math.max(0, value));
    return {
      x: clamp((event.clientX - rect.left) / rect.width),
      y: clamp((event.clientY - rect.top) / rect.height),
    };
  };

  const streamDescription =
    liveScreen ? 'Live · WebRTC'
    : session.paused && supportsFrameStream
      ? frame
        ? `Session paused · last frame at ${time(frame.captured_at)}`
        : 'Screen sharing is paused'
      : !preview && supportsFrameStream
        ? 'Companion stream paused'
        : frame
          ? supportsFrameStream
            ? streamState === 'streaming'
              ? `JPEG preview · Updated at ${time(frame.captured_at)}`
              : `Last frame at ${time(frame.captured_at)} · ${streamState === 'error' ? streamMessage : streamState === 'waiting' ? 'waiting for the next frame' : 'reconnecting to the companion'}`
            : `Updated at ${time(frame.captured_at)}`
          : supportsFrameStream
            ? streamState === 'error'
              ? streamMessage
              : streamState === 'reconnecting'
                ? 'Reconnecting to the companion…'
                : streamState === 'connecting'
                  ? 'Connecting to the companion…'
                  : 'Waiting for screen sharing on the phone'
            : `Controlled by ${session.controller}`;
  const streamEmptyText = !preview
    ? 'Screen stream is paused.'
    : streamState === 'error'
      ? streamMessage
      : streamState === 'reconnecting'
        ? 'Reconnecting to the companion…'
        : 'Start “Share screen” in the companion app. New frames appear here automatically.';
  const sensors = <CompanionSensors camera={showCamera} microphone={showMicrophone} supported={supportsVideo}
    paused={Boolean(session.paused)} streams={companionMode ? streams : {}} failure={liveMediaFailure} />;
  if (!showScreen) return sensors;
  return (<>
    <Card aria-label="Phone screen" className="overflow-hidden">
      <CardHeader className="gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
        <div className="grid gap-1">
          <CardTitle className="text-base">Live device screen</CardTitle>
          <CardDescription className="max-w-2xl">{streamDescription}</CardDescription>
          <div className="flex flex-wrap gap-1.5 pt-1">
            {session.capabilities.map((cap) => (
              <Badge key={cap} variant="outline">
                {labels[cap] || cap}
              </Badge>
            ))}
          </div>
        </div>
        <div className="flex flex-wrap gap-2">
          {supportsVideo && !supportsFrameStream && !session.paused && (
            <Button
              type="button"
              variant={live ? 'secondary' : 'outline'}
              aria-pressed={live}
              onClick={() => setLive((value) => !value)}
            >
              {live ? 'Stop video' : 'Live video'}
            </Button>
          )}
          {session.capabilities.includes('ui_tree') && (
            <Button type="button" variant="outline" onClick={inspectTree} disabled={busy}>
              Inspect UI tree
            </Button>
          )}
          {!live && !controller && !session.paused && (
            <Button
              type="button"
              variant="outline"
              aria-pressed={preview}
              onClick={() => setPreview((value) => !value)}
            >
              {preview
                ? supportsFrameStream
                  ? companionMode && watchSensors ? 'Pause screen preview' : 'Pause stream'
                  : 'Pause preview'
                : supportsFrameStream
                  ? companionMode && watchSensors ? 'Resume screen preview' : 'Resume stream'
                  : 'Auto-refresh'}
            </Button>
          )}
          {!live && (
            <Button type="button" onClick={() => {
              if (liveScreen) setPreview(false);
              void perform();
            }} disabled={busy}>
              <RefreshCw data-icon="inline-start" />
              {busy ? 'Working…' : 'Capture screen'}
            </Button>
          )}
        </div>
      </CardHeader>
      <CardContent className="grid gap-3">
        <div className="phone-screen-stage grid min-h-72 place-items-center overflow-hidden rounded-xl border p-3 sm:min-h-96">
          {live && !session.paused ? (
            <LiveVideo token={token} path={`${base}/video`} />
          ) : liveScreen ? (
            <MediaVideo stream={liveScreen} label="Live phone screen" />
          ) : frame?.image ? (
            <img
              draggable="false"
              alt="Latest authorized screenshot of the phone"
              className={`max-h-[72vh] max-w-full select-none rounded-md object-contain ${controller ? 'cursor-crosshair touch-none' : ''}`}
              src={`data:${frame.image.mime};base64,${frame.image.data}`}
              onPointerDown={(event) => {
                if (controller && !busy) {
                  if ([...pointers.current.values()].every((pointer) => pointer.released)) pointers.current.clear();
                  event.currentTarget.setPointerCapture(event.pointerId);
                  const point = position(event);
                  pointers.current.set(event.pointerId, { start: point, end: point, released: false });
                }
              }}
              onPointerMove={(event) => {
                const pointer = pointers.current.get(event.pointerId);
                if (pointer && !pointer.released) pointer.end = position(event);
              }}
              onPointerCancel={(event) => {
                pointers.current.clear();
              }}
              onPointerUp={(event) => {
                if (!controller) return;
                const pointer = pointers.current.get(event.pointerId);
                if (!pointer) return;
                pointer.end = position(event);
                pointer.released = true;
                const gesturePointers = [...pointers.current.values()];
                if (!gesturePointers.every((item) => item.released)) return;
                pointers.current.clear();
                if (gesturePointers.length > 1) {
                  if (!session.capabilities.includes('multi_touch')) {
                    onError('This device does not advertise multi-touch support.');
                    return;
                  }
                  if (gesturePointers.length > 5) {
                    onError('A multi-touch gesture can include up to five fingers.');
                    return;
                  }
                  const paths = gesturePointers.map(({ start: pathStart, end }) => ({ start: pathStart, end }));
                  let gesture = 'multi_touch';
                  if (paths.length === 2) {
                    const width = Math.max(1, frame.width);
                    const height = Math.max(1, frame.height);
                    const distance = (a: { x: number; y: number }, b: { x: number; y: number }) =>
                      Math.hypot((a.x - b.x) * width, (a.y - b.y) * height);
                    const startDistance = distance(paths[0].start, paths[1].start);
                    const endDistance = distance(paths[0].end, paths[1].end);
                    if (startDistance > 0 && Math.abs(endDistance / startDistance - 1) > 0.08) {
                      gesture = 'pinch';
                    } else if (paths.every((path) => path.start.x === path.end.x && path.start.y === path.end.y)) {
                      gesture = 'two_finger_tap';
                    } else {
                      const angle = (a: { x: number; y: number }, b: { x: number; y: number }) =>
                        Math.atan2((b.y - a.y) * height, (b.x - a.x) * width);
                      const startAngle = angle(paths[0].start, paths[1].start);
                      const endAngle = angle(paths[0].end, paths[1].end);
                      let delta = Math.abs(endAngle - startAngle);
                      if (delta > Math.PI) delta = 2 * Math.PI - delta;
                      if (delta > Math.PI / 12) gesture = 'rotate';
                    }
                  }
                  perform({ type: gesture, paths });
                  return;
                }
                const [{ start: point, end }] = gesturePointers;
                const moved = Math.hypot(point.x - end.x, point.y - end.y) > 0.04;
                const type = touchMode === 'direct'
                  ? moved ? 'swipe' : 'tap'
                  : touchMode;
                if (type === 'swipe' || type === 'drag' || type === 'scroll' || type === 'fling')
                  perform({ type, point, end });
                else perform({ type, point });
              }}
            />
          ) : (
            <Empty className="border-0">
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <Smartphone />
                </EmptyMedia>
                <EmptyTitle>
                  {supportsFrameStream
                    ? session.paused
                      ? 'Screen sharing paused'
                      : 'Waiting for the phone'
                    : 'No screen captured yet'}
                </EmptyTitle>
                <EmptyDescription>
                  {supportsFrameStream ? streamEmptyText : 'Capture a frame to inspect the session.'}
                </EmptyDescription>
              </EmptyHeader>
            </Empty>
          )}
        </div>
        {controller && !live && (
          <div className="flex flex-wrap items-center gap-3 rounded-lg border bg-muted/30 p-3">
            <span className="text-sm font-medium">Screen gesture</span>
            <Select value={touchMode} onValueChange={(value) => setTouchMode(value as TouchMode)}>
              <SelectTrigger className="w-40" aria-label="Screen gesture mode">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectGroup>
                  <SelectItem value="direct">Direct</SelectItem>
                  <SelectItem value="tap">Tap</SelectItem>
                  <SelectItem value="double_tap">Double tap</SelectItem>
                  <SelectItem value="long_press">Long press</SelectItem>
                  <SelectItem value="swipe">Swipe</SelectItem>
                  <SelectItem value="drag">Drag</SelectItem>
                  <SelectItem value="scroll">Scroll</SelectItem>
                  <SelectItem value="fling">Fling</SelectItem>
                </SelectGroup>
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Direct mode maps a tap or swipe. Use two fingers for pinch, rotate, or two-finger tap; more fingers send a multi-touch path.
              {!session.capabilities.includes('multi_touch') && ' Multi-touch is not enabled for this session.'}
            </p>
          </div>
        )}
        {supportsFrameStream && streamMessage && (
          <Alert variant={streamState === 'error' ? 'destructive' : 'default'}>
            <AlertCircle />
            <AlertTitle>
              {streamState === 'error' ? 'Screen stream unavailable' : 'Reconnecting to phone'}
            </AlertTitle>
            <AlertDescription>{streamMessage}</AlertDescription>
          </Alert>
        )}
        {treeOpen && treeObservation && (
          <Card>
            <CardHeader className="flex flex-row items-start justify-between gap-3">
              <div className="grid gap-1">
                <CardTitle className="text-base">Accessible UI tree</CardTitle>
                <CardDescription>
                  {treeObservation.nodes?.length || 0} elements · captured at {time(treeObservation.captured_at)}
                </CardDescription>
              </div>
              <div className="flex gap-2">
                <Button type="button" size="sm" variant="outline" onClick={inspectTree} disabled={busy}>
                  Refresh
                </Button>
                <Button type="button" size="sm" variant="ghost" onClick={() => setTreeOpen(false)}>
                  Hide
                </Button>
              </div>
            </CardHeader>
            <CardContent className="grid gap-3">
              <Input
                value={treeQuery}
                onChange={(event) => setTreeQuery(event.target.value)}
                placeholder="Filter by label, hint, role, or resource ID"
                aria-label="Filter accessible UI elements"
              />
              {!!treeObservation.partial?.length && (
                <Alert>
                  <AlertDescription>{treeObservation.partial.join(' · ')}</AlertDescription>
                </Alert>
              )}
              {shownTreeNodes.length ? (
                <ScrollArea className="h-72 rounded-md border">
                  <div className="divide-y">
                    {shownTreeNodes.map((node) => (
                      <div key={node.id} className="flex items-center gap-3 py-2 pr-3" style={{ paddingLeft: `${12 + treeDepth(node) * 12}px` }}>
                        <div className="grid min-w-0 flex-1 gap-1">
                          <span className="truncate text-sm font-medium">
                            {node.label || node.hint || node.resource_id || node.role || 'UI element'}
                          </span>
                          <span className="truncate text-xs text-muted-foreground">
                            {[node.role, node.resource_id, `${node.bounds.x},${node.bounds.y} · ${node.bounds.width}×${node.bounds.height} px`]
                              .filter(Boolean)
                              .join(' · ')}
                          </span>
                          <div className="flex flex-wrap gap-1">
                            {node.clickable && <Badge variant="secondary">Clickable</Badge>}
                            {node.long_clickable && <Badge variant="outline">Long press</Badge>}
                            {node.editable && <Badge variant="outline">Editable</Badge>}
                            {node.focusable && <Badge variant="outline">Focusable</Badge>}
                            {node.focused && <Badge variant="secondary">Focused</Badge>}
                            {node.checkable && <Badge variant="outline">Checkable</Badge>}
                            {node.scrollable && <Badge variant="outline">Scrollable</Badge>}
                            {node.checked && <Badge variant="outline">Checked</Badge>}
                            {node.selected && <Badge variant="outline">Selected</Badge>}
                            {node.protected && <Badge variant="destructive">Protected</Badge>}
                            {!node.enabled && <Badge variant="outline">Disabled</Badge>}
                            {!node.visible && <Badge variant="outline">Hidden</Badge>}
                          </div>
                        </div>
                        <div className="flex shrink-0 flex-wrap justify-end gap-1">
                          <Button
                            type="button"
                            size="sm"
                            variant="outline"
                            disabled={busy || !controller || !node.enabled || !node.visible}
                            onClick={() =>
                              perform({ type: 'tap', node_id: node.id, frame_id: treeObservation.frame_id })
                            }
                          >
                            Tap
                          </Button>
                          {node.long_clickable && (
                            <Button
                              type="button"
                              size="sm"
                              variant="outline"
                              disabled={busy || !controller || !node.enabled || !node.visible}
                              onClick={() =>
                                perform({ type: 'long_press', node_id: node.id, frame_id: treeObservation.frame_id })
                              }
                            >
                              Hold
                            </Button>
                          )}
                          {node.scrollable && (
                            <>
                              <Button
                                type="button"
                                size="sm"
                                variant="ghost"
                                aria-label="Swipe up within this element"
                                disabled={busy || !controller || !node.enabled || !node.visible}
                                onClick={() =>
                                  perform({ type: 'scroll', node_id: node.id, direction: 'up', frame_id: treeObservation.frame_id })
                                }
                              >
                                ↑
                              </Button>
                              <Button
                                type="button"
                                size="sm"
                                variant="ghost"
                                aria-label="Swipe down within this element"
                                disabled={busy || !controller || !node.enabled || !node.visible}
                                onClick={() =>
                                  perform({ type: 'scroll', node_id: node.id, direction: 'down', frame_id: treeObservation.frame_id })
                                }
                              >
                                ↓
                              </Button>
                            </>
                          )}
                        </div>
                      </div>
                    ))}
                  </div>
                </ScrollArea>
              ) : (
                <Empty className="border-0">
                  <EmptyHeader>
                    <EmptyTitle>{treeNodes.length && normalizedTreeQuery ? 'No matching elements' : 'No accessible elements found'}</EmptyTitle>
                    <EmptyDescription>
                      {treeNodes.length && normalizedTreeQuery
                        ? 'Try a shorter label, role, hint, or resource ID.'
                        : 'The current device may not expose a UI hierarchy, or this screen has no accessible controls.'}
                    </EmptyDescription>
                  </EmptyHeader>
                  {!!treeNodes.length && !!normalizedTreeQuery && (
                    <EmptyContent>
                      <Button type="button" variant="outline" onClick={() => setTreeQuery('')}>Clear filter</Button>
                    </EmptyContent>
                  )}
                </Empty>
              )}
              {matchingTreeNodes.length > shownTreeNodes.length && (
                <p className="text-xs text-muted-foreground">Showing {shownTreeNodes.length} of {matchingTreeNodes.length} matching elements. Refine the filter or use MCP to query the full tree.</p>
              )}
            </CardContent>
          </Card>
        )}
        {controller && frame && !live && (
          <div className="flex flex-wrap items-center gap-2 rounded-lg border bg-muted/30 p-3">
            <span className="mr-1 text-sm font-medium">Phone controls</span>
            <Button
              type="button"
              size="sm"
              variant="outline"
              onClick={() => perform({ type: 'key', key: 'home' })}
              disabled={busy}
            >
              Home
            </Button>
            {supportsNavKeys && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => perform({ type: 'key', key: 'back' })}
                disabled={busy}
              >
                Back
              </Button>
            )}
            {supportsNavKeys && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => perform({ type: 'key', key: 'notification_shade' })}
                disabled={busy}
              >
                Notifications
              </Button>
            )}
            {supportsNavKeys && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => perform({ type: 'key', key: 'quick_settings' })}
                disabled={busy}
              >
                Quick settings
              </Button>
            )}
            {supportsNavKeys && (
              <Button
                type="button"
                size="sm"
                variant="outline"
                onClick={() => perform({ type: 'key', key: 'overview' })}
                disabled={busy}
              >
                Overview
              </Button>
            )}
            {supportsNavKeys && (
              <div className="flex min-w-[min(100%,18rem)] flex-1 gap-2">
                <Select value={hardwareKey} onValueChange={(value) => setHardwareKey(value as AndroidKey)}>
                  <SelectTrigger className="min-w-40 flex-1" aria-label="Android hardware key">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectGroup>
                      <SelectItem value="enter">Enter</SelectItem>
                      <SelectItem value="tab">Tab</SelectItem>
                      <SelectItem value="escape">Escape</SelectItem>
                      <SelectItem value="menu">Menu</SelectItem>
                      <SelectItem value="dpad_up">D-pad up</SelectItem>
                      <SelectItem value="dpad_down">D-pad down</SelectItem>
                      <SelectItem value="dpad_left">D-pad left</SelectItem>
                      <SelectItem value="dpad_right">D-pad right</SelectItem>
                      <SelectItem value="volume_up">Volume up</SelectItem>
                      <SelectItem value="volume_down">Volume down</SelectItem>
                      <SelectItem value="volume_mute">Mute</SelectItem>
                    </SelectGroup>
                  </SelectContent>
                </Select>
                <Button type="button" variant="outline" onClick={() => perform({ type: 'key', key: hardwareKey })} disabled={busy}>
                  Press key
                </Button>
              </div>
            )}
            {session.capabilities.includes('text') && (
              <form
                className="flex min-w-[min(100%,20rem)] flex-1 gap-2"
                onSubmit={(event) => {
                  event.preventDefault();
                  perform({ type: 'type', text });
                  setText('');
                }}
              >
                <Input
                  aria-label="Text to type on the phone"
                  placeholder="Type text on device"
                  value={text}
                  onChange={(event) => setText(event.target.value)}
                />
                <Button type="submit" disabled={busy || !text}>Send text</Button>
              </form>
            )}
          </div>
        )}
      </CardContent>
    </Card>
    {sensors}
  </>);
}

createRoot(document.getElementById('root')!).render(<App />);
