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
} from 'lucide-react';
import { ThemeProvider, useTheme } from 'next-themes';
import { api, beginLogin, completeLogin, exchangeLaunchTicket, labels } from './api';
import type { Actor, Audit, Device, Grant, Observation, OIDC, Session, SystemInfo } from './api';
import { AddDevicePanel } from './AddDevicePanel';
import { LiveVideo } from './LiveVideo';
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
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field';
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
import './index.css';

type Tab = ConsolePage;
const time = (date: string) => new Date(date).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' });
const errorText = (error: unknown) => (error instanceof Error ? error.message : 'Something went wrong.');

function App() {
  const [token, setToken] = useState(() => sessionStorage.getItem('mobile-use.token') || '');
  const [draftToken, setDraftToken] = useState('');
  const [oidc, setOIDC] = useState<OIDC>();
  const [systemInfo, setSystemInfo] = useState<SystemInfo>();
  const [actor, setActor] = useState<Actor>();
  const [devices, setDevices] = useState<Device[]>([]);
  const [sessions, setSessions] = useState<Session[]>([]);
  const [audits, setAudits] = useState<Audit[]>([]);
  const [tab, setTab] = useState<Tab>('Overview');
  const [selected, setSelected] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [captureRevision, setCaptureRevision] = useState(0);
  const [showAddDevice, setShowAddDevice] = useState(false);

  const signedIn = useCallback((value: string) => {
    sessionStorage.setItem('mobile-use.token', value);
    setToken(value);
    setDraftToken('');
  }, []);

  useEffect(() => {
    const hash = location.hash;
    const launchTicket = hash.startsWith('#launch=') ? hash.slice('#launch='.length) : '';
    if (launchTicket) history.replaceState(null, '', '/');

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
      .catch((error) => setError(errorText(error)));
  }, [signedIn]);

  const refresh = useCallback(
    async (credential: string, identity: Actor) => {
      const [nextDevices, nextSessions] = await Promise.all([
        api<Device[]>(credential, '/v1/devices'),
        api<Session[]>(credential, '/v1/sessions'),
      ]);
      setDevices(nextDevices);
      setSessions(nextSessions);
      setSelected((current) =>
        nextDevices.some((device) => device.id === current) ? current : nextDevices[0]?.id || '',
      );
      if (tab === 'Audit' && ['admin', 'auditor'].includes(identity.role)) {
        setAudits(await api<Audit[]>(credential, '/v1/audit'));
      }
    },
    [tab],
  );

  useEffect(() => {
    if (!token) return;
    let ignore = false;
    api<Actor>(token, '/v1/me')
      .then(async (identity) => {
        if (ignore) return;
        setActor(identity);
        api<SystemInfo>(token, '/v1/system/info')
          .then((info) => {
            if (!ignore) setSystemInfo(info);
          })
          .catch(() => undefined);
        // A failed inventory/audit refresh (this effect re-runs on every tab change) is not
        // an authentication failure and must not drop the signed-in identity.
        await refresh(token, identity).catch((error) => {
          if (!ignore) setError(errorText(error));
        });
      })
      .catch((error) => {
        if (!ignore) {
          setActor(undefined);
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
      if (!document.hidden) refresh(token, actor).catch((error) => setError(errorText(error)));
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
    setSystemInfo(undefined);
    setDevices([]);
    setSessions([]);
    setAudits([]);
    setSelected('');
    setTab('Overview');
    setShowAddDevice(false);
    setError('');
  }

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
  const deviceSessions = sessions.filter((item) => item.device_id === selected);
  const active = deviceSessions.find((item) => item.state === 'active');
  const change = (session: Session, verb: string) =>
    run(() => api(token, `/v1/sessions/${session.id}/${verb}`, 'POST'));
  const setPage = (page: ConsolePage) => {
    setTab(page);
    setShowAddDevice(false);
  };
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
                <Badge variant="outline" className="hidden sm:inline-flex">
                  {actor.workspace === 'local' ? 'Local' : actor.workspace}
                </Badge>
                <ThemeToggle />
                <Button
                  variant="outline"
                  size="sm"
                  disabled={busy}
                  onClick={() => run(() => refresh(token, actor))}
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
                    <Button size="sm" variant="outline" onClick={() => setError('')}>
                      Dismiss
                    </Button>
                  </AlertDescription>
                </Alert>
              )}
              {tab === 'Overview' && (
                <Overview
                  devices={devices}
                  sessions={sessions}
                  systemInfo={systemInfo}
                  onNavigate={setPage}
                  onAddDevice={() => setShowAddDevice(true)}
                />
              )}
              {tab === 'Devices' && (
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
                                onClick={() => setSelected(item.id)}
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
                          <Badge variant={device.online ? 'secondary' : 'outline'}>
                            {device.online ? 'Online' : 'Offline'}
                          </Badge>
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
                              supportsNavKeys={device.mode !== 'companion'}
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
                          {device.enabled && device.online && !active && actor.role !== 'auditor' && (
                            <StartForm
                              key={`start-${device.id}`}
                              device={device}
                              busy={busy}
                              submit={(body) => run(() => api(token, '/v1/sessions', 'POST', body))}
                            />
                          )}
                          {manage && (
                            <EnrollmentForm
                              key={device.id}
                              actor={actor}
                              device={device}
                              busy={busy}
                              submit={(body) =>
                                run(() => api(token, `/v1/devices/${device.id}`, 'PUT', body))
                              }
                              onUnpair={
                                device.mode === 'companion'
                                  ? () => run(() => api(token, `/v1/companion/${device.id}`, 'DELETE'))
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
              {tab === 'Sessions' && (
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
                      setSelected(id);
                      setPage('Devices');
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
                  <CardHeader>
                    <CardTitle>Workspace audit</CardTitle>
                    <CardDescription>
                      Access decisions and device operations recorded for this workspace.
                    </CardDescription>
                  </CardHeader>
                  <CardContent>
                    {audits.length === 0 ? (
                      <Empty>
                        <EmptyHeader>
                          <EmptyMedia variant="icon">
                            <ShieldCheck />
                          </EmptyMedia>
                          <EmptyTitle>No audit events yet</EmptyTitle>
                          <EmptyDescription>
                            Events appear as devices connect and sessions are requested.
                          </EmptyDescription>
                        </EmptyHeader>
                      </Empty>
                    ) : (
                      <ScrollArea className="max-h-[68vh] rounded-md border">
                        <Table>
                          <TableHeader className="sticky top-0 z-10 bg-card">
                            <TableRow>
                              <TableHead>Time</TableHead>
                              <TableHead>Event</TableHead>
                              <TableHead>Identity</TableHead>
                              <TableHead>Result</TableHead>
                            </TableRow>
                          </TableHeader>
                          <TableBody>
                            {audits.map((item, index) => (
                              <TableRow key={`${item.at}-${index}`}>
                                <TableCell className="whitespace-nowrap">
                                  {new Date(item.at).toLocaleString()}
                                </TableCell>
                                <TableCell>{item.event.replaceAll('_', ' ')}</TableCell>
                                <TableCell>{item.actor}</TableCell>
                                <TableCell>{item.code?.replaceAll('_', ' ') || '—'}</TableCell>
                              </TableRow>
                            ))}
                          </TableBody>
                        </Table>
                      </ScrollArea>
                    )}
                  </CardContent>
                </Card>
              )}
            </main>
            <Sheet open={showAddDevice} onOpenChange={setShowAddDevice}>
              <SheetContent side="right" className="w-full overflow-y-auto sm:max-w-3xl">
                <SheetHeader className="sr-only">
                  <SheetTitle>Add a device</SheetTitle>
                  <SheetDescription>
                    Choose companion pairing, Wi-Fi ADB, or remote connection.
                  </SheetDescription>
                </SheetHeader>
                <div className="pb-6">
                  <AddDevicePanel
                    token={token}
                    actor={actor}
                    systemInfo={systemInfo}
                    companionEnabled={Boolean(oidc?.companion_enabled)}
                    onClose={() => setShowAddDevice(false)}
                    onNavigateDownloads={() => setPage('Downloads')}
                    onDeviceAdded={async (deviceId) => {
                      await refresh(token, actor);
                      if (deviceId) setSelected(deviceId);
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
  const supported = Object.keys(device.capabilities).filter((cap) => device.capabilities[cap]);
  const [grantList, setGrantList] = useState<Grant[]>(() =>
    (device.grants || []).map((g) => ({ subject: g.subject, capabilities: [...g.capabilities] })),
  );
  const [newSubject, setNewSubject] = useState('');
  const [confirmRevoke, setConfirmRevoke] = useState(false);
  const [confirmUnpair, setConfirmUnpair] = useState(false);

  function addAgent(subject: string) {
    const clean = subject.trim();
    if (!clean || grantList.some((g) => g.subject === clean)) return;
    setGrantList((current) => [...current, { subject: clean, capabilities: [...supported] }]);
    setNewSubject('');
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
    const cleaned = grantList
      .map((g) => ({
        subject: g.subject.trim(),
        capabilities: g.capabilities.filter((c) => supported.includes(c)),
      }))
      .filter((g) => g.subject && g.capabilities.length > 0);
    submit({ label: name, owner, profile, grants: cleaned, enabled: true });
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
        <form onSubmit={onboard} className="grid gap-5">
          <FieldGroup className="sm:flex-row">
            <Field className="sm:flex-1">
              <FieldLabel htmlFor="device-name">Device name</FieldLabel>
              <Input
                id="device-name"
                required
                maxLength={128}
                value={name}
                onChange={(event) => setName(event.target.value)}
              />
            </Field>
            <Field className="sm:flex-1">
              <FieldLabel htmlFor="device-owner">Owner identity</FieldLabel>
              <Input
                id="device-owner"
                required
                value={owner}
                onChange={(event) => setOwner(event.target.value)}
              />
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
                onChange={(event) => setNewSubject(event.target.value)}
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
          </div>
          <div className="flex flex-wrap gap-2">
            <Button disabled={busy}>{device.enabled ? 'Save access' : 'Enroll device'}</Button>
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
  submit: (value: unknown) => void;
}) {
  const [purpose, setPurpose] = useState('');
  const options = Object.keys(device.capabilities).filter((cap) => device.capabilities[cap]);
  const [selected, setSelected] = useState(
    options.filter((cap) => ['screen', 'touch', 'ui_tree', 'text'].includes(cap)),
  );
  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">Request a device session</CardTitle>
        <CardDescription>
          The phone owner reviews the purpose and selected capabilities before access starts.
        </CardDescription>
      </CardHeader>
      <CardContent>
        <form
          className="grid gap-5"
          onSubmit={(event) => {
            event.preventDefault();
            submit({ device_id: device.id, purpose, capabilities: selected });
          }}
        >
          <Field>
            <FieldLabel htmlFor="session-purpose">Purpose</FieldLabel>
            <Textarea
              id="session-purpose"
              minLength={3}
              maxLength={256}
              required
              rows={2}
              placeholder="What do you need to do on this phone?"
              value={purpose}
              onChange={(event) => setPurpose(event.target.value)}
            />
          </Field>
          <fieldset className="grid gap-3">
            <legend className="text-sm font-medium">Requested capabilities</legend>
            <p className="text-sm text-muted-foreground">Select the smallest set needed for this task.</p>
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
          </fieldset>
          <div>
            <Button disabled={busy || selected.length === 0}>
              Request access <ArrowRight data-icon="inline-end" />
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
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [preview, setPreview] = useState(Boolean(supportsFrameStream));
  const [streamState, setStreamState] = useState<
    'idle' | 'connecting' | 'waiting' | 'streaming' | 'reconnecting' | 'error'
  >('idle');
  const [streamMessage, setStreamMessage] = useState('');
  const [live, setLive] = useState(false);
  const [pageVisible, setPageVisible] = useState(() => !document.hidden);
  const start = useRef<{ x: number; y: number } | undefined>(undefined);
  const base = `/v1/sessions/${session.id}`;
  const owner = session.owner === actor.subject && actor.role !== 'agent';
  const controller =
    !session.paused && session.controller === actor.subject && session.capabilities.includes('touch');

  const observe = useCallback(async () => {
    for (let attempt = 0; ; attempt += 1) {
      const result = await api<{ observation?: Observation; error?: { code: string; message: string } }>(
        token,
        `${base}/operations`,
        'POST',
        { kind: 'observe', observe: { screen: true, tree: false } },
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

  useEffect(() => {
    const updateVisibility = () => setPageVisible(!document.hidden);
    document.addEventListener('visibilitychange', updateVisibility);
    return () => document.removeEventListener('visibilitychange', updateVisibility);
  }, []);

  useEffect(() => {
    if (!supportsFrameStream || !preview || !pageVisible || session.paused) {
      setStreamState('idle');
      setStreamMessage('');
      return;
    }
    const abort = new AbortController();
    let closed = false;
    let retryDelay = 500;
    const connect = async () => {
      while (!closed) {
        try {
          setStreamMessage('');
          setStreamState((current) => (current === 'streaming' ? current : 'connecting'));
          const response = await fetch(`/v1/companion/sessions/${session.id}/frames`, {
            headers: { Authorization: `Bearer ${token}`, Accept: 'text/event-stream' },
            signal: abort.signal,
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
          while (!closed) {
            const { value, done } = await reader.read();
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
                  if (next.image) setFrame(next);
                  setStreamState('streaming');
                } else if (event === 'status') {
                  setStreamState('waiting');
                }
              }
              boundary = buffer.indexOf('\n\n');
            }
          }
          if (!closed) throw new Error('The companion screen stream disconnected.');
        } catch (error) {
          if (closed || abort.signal.aborted) return;
          setStreamMessage(errorText(error));
          setStreamState('reconnecting');
          await new Promise((resolve) => window.setTimeout(resolve, retryDelay));
          retryDelay = Math.min(3000, retryDelay * 2);
        }
      }
    };
    void connect();
    return () => {
      closed = true;
      abort.abort();
    };
  }, [pageVisible, preview, session.id, session.paused, supportsFrameStream, token]);

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
    start.current = undefined;
    setFrame(undefined);
  }, [captureRevision, session.paused]);

  async function perform(action?: Record<string, unknown>) {
    if (busy) return;
    setBusy(true);
    onError('');
    try {
      if (action && frame) {
        const result = await api<{ error?: { message: string } }>(token, `${base}/operations`, 'POST', {
          id: crypto.randomUUID(),
          kind: 'act',
          action: { ...action, frame_id: frame.frame_id },
        });
        if (result.error) throw new Error(result.error.message);
      }
      setFrame(await observe());
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

  const show =
    session.capabilities.includes('screen') && (owner || session.requester === actor.subject || controller);
  const streamDescription =
    session.paused && supportsFrameStream
      ? frame
        ? `Session paused · last frame at ${time(frame.captured_at)}`
        : 'Screen sharing is paused'
      : !preview && supportsFrameStream
        ? 'Companion stream paused'
        : frame
          ? supportsFrameStream
            ? streamState === 'streaming'
              ? `Live · Updated at ${time(frame.captured_at)}`
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
  if (!show) return null;
  return (
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
          {supportsVideo && !session.paused && (
            <Button
              type="button"
              variant={live ? 'secondary' : 'outline'}
              aria-pressed={live}
              onClick={() => setLive((value) => !value)}
            >
              {live ? 'Stop video' : 'Live video'}
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
                  ? 'Pause stream'
                  : 'Pause preview'
                : supportsFrameStream
                  ? 'Resume stream'
                  : 'Auto-refresh'}
            </Button>
          )}
          {!live && (
            <Button type="button" onClick={() => perform()} disabled={busy}>
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
          ) : frame?.image ? (
            <img
              draggable="false"
              alt="Latest authorized screenshot of the phone"
              className={`max-h-[72vh] max-w-full select-none rounded-md object-contain ${controller ? 'cursor-crosshair touch-none' : ''}`}
              src={`data:${frame.image.mime};base64,${frame.image.data}`}
              onPointerDown={(event) => {
                if (controller && !busy) {
                  event.currentTarget.setPointerCapture(event.pointerId);
                  start.current = position(event);
                }
              }}
              onPointerCancel={() => {
                start.current = undefined;
              }}
              onPointerUp={(event) => {
                if (!controller || !start.current) return;
                const point = start.current;
                start.current = undefined;
                const end = position(event);
                if (Math.hypot(point.x - end.x, point.y - end.y) > 0.04)
                  perform({ type: 'swipe', point, end });
                else perform({ type: 'tap', point });
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
        {supportsFrameStream && streamMessage && (
          <Alert variant={streamState === 'error' ? 'destructive' : 'default'}>
            <AlertCircle />
            <AlertTitle>
              {streamState === 'error' ? 'Screen stream unavailable' : 'Reconnecting to phone'}
            </AlertTitle>
            <AlertDescription>{streamMessage}</AlertDescription>
          </Alert>
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
                onClick={() => perform({ type: 'key', key: 'overview' })}
                disabled={busy}
              >
                Overview
              </Button>
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
                <Button disabled={busy || !text}>Send text</Button>
              </form>
            )}
          </div>
        )}
      </CardContent>
    </Card>
  );
}

createRoot(document.getElementById('root')!).render(<App />);
