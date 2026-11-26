import { useCallback, useEffect, useState } from 'react';
import type { FormEvent } from 'react';
import {
  Cable,
  Check,
  Copy,
  ExternalLink,
  Globe2,
  QrCode as QrCodeIcon,
  RefreshCw,
  ShieldCheck,
  Smartphone,
  Wifi,
} from 'lucide-react';
import { api } from './api';
import type {
  Actor,
  CompanionEndpoint,
  PairingSession,
  PairingStatus,
  SystemInfo,
  WirelessAdbDiscovery,
} from './api';
import { QrCode } from './qr';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Checkbox } from '@/components/ui/checkbox';
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

type AddTab = 'companion' | 'wifi-adb' | 'remote';

function shortFingerprint(hex?: string): string {
  if (!hex) return '';
  const clean = hex.trim().toUpperCase();
  if (clean.length < 16) return clean;
  return (
    clean
      .slice(0, 16)
      .match(/.{1,2}/g)
      ?.join(':') || clean
  );
}

export function CopyButton({ value, label = 'Copy' }: { value: string; label?: string }) {
  const [copied, setCopied] = useState(false);
  async function copy() {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      window.setTimeout(() => setCopied(false), 1500);
    } catch {
      setCopied(false);
    }
  }
  return (
    <Button type="button" variant="outline" size="sm" onClick={copy} aria-label={copied ? 'Copied' : label}>
      {copied ? <Check data-icon="inline-start" /> : <Copy data-icon="inline-start" />}
      {copied ? 'Copied' : label}
    </Button>
  );
}

export function AddDevicePanel({
  token,
  actor,
  systemInfo,
  companionEnabled,
  onClose,
  onDeviceAdded,
  onNavigateDownloads,
}: {
  token: string;
  actor: Actor;
  systemInfo?: SystemInfo;
  companionEnabled: boolean;
  onClose: () => void;
  onDeviceAdded: (deviceId?: string) => Promise<void>;
  onNavigateDownloads?: () => void;
}) {
  const canPairCompanion = companionEnabled && ['admin', 'operator', 'owner'].includes(actor.role);
  const supportsWifiAdb = Boolean(
    systemInfo?.wireless_adb_supported && ['admin', 'operator'].includes(actor.role),
  );
  const defaultTab: AddTab = canPairCompanion ? 'companion' : supportsWifiAdb ? 'wifi-adb' : 'remote';
  const [tab, setTab] = useState<AddTab>(defaultTab);
  const [pairing, setPairing] = useState<PairingSession>();
  const [pairStatus, setPairStatus] = useState<PairingStatus>();
  const [selectedOrigin, setSelectedOrigin] = useState('');
  const [autoEnroll, setAutoEnroll] = useState(
    systemInfo?.mode === 'workstation' || actor.workspace === 'local',
  );
  const [pairLoading, setPairLoading] = useState(false);
  const [pairError, setPairError] = useState('');
  const [nowMs, setNowMs] = useState(() => Date.now());
  const [adbDiscovery, setAdbDiscovery] = useState<WirelessAdbDiscovery>();
  const [adbScanning, setAdbScanning] = useState(false);
  const [pairAddr, setPairAddr] = useState('');
  const [pairCode, setPairCode] = useState('');
  const [connectAddr, setConnectAddr] = useState('');
  const [adbBusy, setAdbBusy] = useState(false);
  const [adbNotice, setAdbNotice] = useState('');
  const [adbError, setAdbError] = useState('');

  const createPairing = useCallback(
    async (originOverride?: string, enrollFlag?: boolean) => {
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
    },
    [token, autoEnroll, selectedOrigin],
  );

  useEffect(() => {
    if (tab === 'companion' && canPairCompanion && !pairing && !pairLoading && !pairError)
      void createPairing();
  }, [tab, canPairCompanion, pairing, pairLoading, pairError, createPairing]);

  useEffect(() => {
    if (!pairing || pairStatus?.state === 'paired' || pairStatus?.state === 'expired') return;
    let cancelled = false;
    let reported = false;
    const timer = window.setInterval(async () => {
      setNowMs(Date.now());
      try {
        const next = await api<PairingStatus>(token, `/v1/companion/pairing/${pairing.id}`);
        if (cancelled) return;
        setPairStatus(next);
        if (next.state === 'paired' && !reported) {
          reported = true;
          await onDeviceAdded(next.device_id);
        }
      } catch {
        /* retry transient status failures */
      }
    }, 1500);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [pairing, pairStatus?.state, token, onDeviceAdded]);

  useEffect(() => {
    const timer = window.setInterval(() => setNowMs(Date.now()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  const scanWifiAdb = useCallback(async () => {
    setAdbScanning(true);
    setAdbError('');
    try {
      setAdbDiscovery(await api<WirelessAdbDiscovery>(token, '/v1/local/adb/discover'));
    } catch (err) {
      setAdbError(err instanceof Error ? err.message : 'Could not scan for Wi-Fi ADB devices.');
    } finally {
      setAdbScanning(false);
    }
  }, [token]);

  useEffect(() => {
    if (tab === 'wifi-adb' && supportsWifiAdb && !adbDiscovery && !adbScanning) void scanWifiAdb();
  }, [tab, supportsWifiAdb, adbDiscovery, adbScanning, scanWifiAdb]);

  async function submitWifiPair(event: FormEvent) {
    event.preventDefault();
    setAdbBusy(true);
    setAdbNotice('');
    setAdbError('');
    try {
      const result = await api<{ message: string }>(token, '/v1/local/adb/pair', 'POST', {
        address: pairAddr.trim(),
        code: pairCode.trim(),
      });
      setAdbNotice(
        result.message || 'Paired successfully. Connect using the Wireless debugging IP and port.',
      );
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
      const result = await api<{ message: string }>(token, '/v1/local/adb/connect', 'POST', {
        address: address.trim(),
      });
      setAdbNotice(result.message || `Connected to ${address}.`);
      await onDeviceAdded();
    } catch (err) {
      setAdbError(err instanceof Error ? err.message : 'Could not connect over Wi-Fi.');
    } finally {
      setAdbBusy(false);
    }
  }

  const endpoints: CompanionEndpoint[] = pairing?.origins || systemInfo?.companion_origins || [];
  const expiresSec = pairing
    ? Math.max(0, Math.floor((new Date(pairing.expires_at).getTime() - nowMs) / 1000))
    : 0;
  const countdown = `${Math.floor(expiresSec / 60)}:${String(expiresSec % 60).padStart(2, '0')}`;
  const fpShort = shortFingerprint(pairing?.fingerprint || systemInfo?.companion_fingerprint);
  const remoteSetup = `mobile-use remote set --url ${systemInfo?.origin || 'https://mobile-use.example.com'} --token <AGENT_TOKEN>\nmobile-use pair`;

  return (
    <Card aria-label="Add a phone or device">
      <CardHeader className="grid-cols-[minmax(0,1fr)_auto] items-start justify-between gap-4">
        <div className="grid gap-1">
          <CardTitle>Add a device</CardTitle>
          <CardDescription>Choose how the phone can reach this workspace.</CardDescription>
        </div>
        <Button type="button" variant="ghost" onClick={onClose}>
          Close
        </Button>
      </CardHeader>
      <CardContent className="grid gap-5">
        <Tabs value={tab} onValueChange={(value) => setTab(value as AddTab)}>
          <TabsList className="grid h-auto w-full grid-cols-1 sm:grid-cols-3">
            {canPairCompanion && (
              <TabsTrigger value="companion">
                <Smartphone data-icon="inline-start" />
                Android companion
              </TabsTrigger>
            )}
            {supportsWifiAdb && (
              <TabsTrigger value="wifi-adb">
                <Wifi data-icon="inline-start" />
                Wi-Fi ADB
              </TabsTrigger>
            )}
            <TabsTrigger value="remote">
              <Globe2 data-icon="inline-start" />
              Remote / iPhone
            </TabsTrigger>
          </TabsList>
          {canPairCompanion && (
            <TabsContent value="companion" className="mt-5">
              {pairError && (
                <Alert variant="destructive" className="mb-4">
                  <AlertTitle>Pairing could not start</AlertTitle>
                  <AlertDescription>{pairError}</AlertDescription>
                </Alert>
              )}
              {pairStatus?.state === 'paired' ? (
                <Card className="border-primary/30 bg-primary/5">
                  <CardContent className="flex flex-col gap-4 p-6 sm:flex-row sm:items-center">
                    <ShieldCheck className="size-8 shrink-0 text-primary" />
                    <div className="grid flex-1 gap-1">
                      <p className="font-semibold">{pairStatus.label || 'Android phone'} is paired</p>
                      <p className="text-sm text-muted-foreground">
                        {pairStatus.auto_enrolled
                          ? 'It is enrolled and ready for approved agent sessions.'
                          : 'Review enrollment and agent access in the device details.'}
                      </p>
                    </div>
                    <div className="flex gap-2">
                      <Button onClick={() => void createPairing()}>Pair another</Button>
                      <Button variant="outline" onClick={onClose}>
                        Done
                      </Button>
                    </div>
                  </CardContent>
                </Card>
              ) : (
                <div className="grid gap-5 xl:grid-cols-[minmax(280px,360px)_minmax(0,1fr)]">
                  <Card className="bg-muted/30">
                    <CardContent className="grid justify-items-center gap-4 p-5">
                      <div className="grid min-h-56 w-full place-items-center rounded-xl border bg-background p-3">
                        {pairing?.qr_uri ? (
                          <QrCode
                            value={pairing.qr_uri}
                            label="QR code for pairing Android companion"
                            size={212}
                          />
                        ) : (
                          <div className="grid justify-items-center gap-3 text-muted-foreground">
                            <QrCodeIcon className="size-10" />
                            <span>{pairLoading ? 'Preparing secure QR code…' : 'QR code unavailable'}</span>
                          </div>
                        )}
                      </div>
                      <div className="grid w-full gap-1 text-center">
                        <span className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                          Pairing code
                        </span>
                        <div className="flex items-center justify-center gap-2">
                          <code className="text-2xl font-semibold tracking-[0.18em]">
                            {pairing?.code || '---- ----'}
                          </code>
                          {pairing?.code && <CopyButton value={pairing.code} />}
                        </div>
                        <span className="text-sm text-muted-foreground">
                          {pairStatus?.state === 'expired' || (pairing && expiresSec === 0)
                            ? 'Code expired. Generate a fresh one.'
                            : pairing
                              ? `Waiting for phone · ${countdown} remaining`
                              : 'Creating a pairing code…'}
                        </span>
                      </div>
                    </CardContent>
                  </Card>
                  <div className="grid content-start gap-4">
                    <div className="grid gap-2">
                      <h3 className="font-semibold">Connect in three steps</h3>
                      <ol className="grid gap-2 text-sm text-muted-foreground">
                        <li className="flex gap-3">
                          <Badge variant="secondary">1</Badge>
                          <span>Install and open mobile-use on Android.</span>
                        </li>
                        <li className="flex gap-3">
                          <Badge variant="secondary">2</Badge>
                          <span>
                            Tap <strong className="text-foreground">Scan QR code</strong>, or choose this
                            computer in Nearby on Wi-Fi.
                          </span>
                        </li>
                        <li className="flex gap-3">
                          <Badge variant="secondary">3</Badge>
                          <span>
                            Confirm the server address. Local Wi-Fi uses pinned TLS; remote workspaces use
                            HTTPS.
                          </span>
                        </li>
                      </ol>
                    </div>
                    {endpoints.length > 1 && (
                      <Field>
                        <FieldLabel>Network interface</FieldLabel>
                        <Select
                          value={selectedOrigin || pairing?.origin || ''}
                          onValueChange={(next) => {
                            if (!next) return;
                            setSelectedOrigin(next);
                            void createPairing(next, autoEnroll);
                          }}
                          disabled={pairLoading}
                        >
                          <SelectTrigger>
                            <SelectValue placeholder="Choose an interface" />
                          </SelectTrigger>
                          <SelectContent>
                            <SelectGroup>
                              {endpoints.map((ep) => (
                                <SelectItem key={ep.origin} value={ep.origin}>
                                  {ep.origin}
                                  {ep.interface ? ` · ${ep.interface}` : ''}
                                  {ep.primary ? ' · Primary' : ''}
                                </SelectItem>
                              ))}
                            </SelectGroup>
                          </SelectContent>
                        </Select>
                      </Field>
                    )}
                    <Card>
                      <CardContent className="grid gap-3 p-4 text-sm">
                        <div className="flex items-start justify-between gap-3">
                          <span className="text-muted-foreground">Server address</span>
                          <div className="flex items-center gap-2">
                            <code className="break-all text-right">
                              {pairing?.origin || systemInfo?.companion_origin || 'Detecting…'}
                            </code>
                            {pairing?.origin && <CopyButton value={pairing.origin} />}
                          </div>
                        </div>
                        <div className="flex items-center justify-between gap-3">
                          <span className="text-muted-foreground">Connection</span>
                          <Badge variant="outline">
                            {(pairing?.mode || systemInfo?.companion_mode) === 'lan'
                              ? 'Local Wi-Fi · pinned TLS'
                              : 'Remote HTTPS'}
                          </Badge>
                        </div>
                        {fpShort && (
                          <div className="flex items-center justify-between gap-3">
                            <span className="text-muted-foreground">TLS fingerprint</span>
                            <code>{fpShort}</code>
                          </div>
                        )}
                      </CardContent>
                    </Card>
                    {['admin', 'operator'].includes(actor.role) && (
                      <Field orientation="horizontal" className="items-start">
                        <Checkbox
                          id="auto-enroll"
                          checked={autoEnroll}
                          disabled={pairLoading}
                          onCheckedChange={(checked) => {
                            const next = checked === true;
                            setAutoEnroll(next);
                            void createPairing(selectedOrigin || undefined, next);
                          }}
                        />
                        <div className="grid gap-1">
                          <FieldLabel htmlFor="auto-enroll">Enroll for local MCP agent</FieldLabel>
                          <FieldDescription>
                            Creates a local-agent grant so Codex can request approved sessions.
                          </FieldDescription>
                        </div>
                      </Field>
                    )}
                    <div className="flex flex-wrap gap-2">
                      <Button type="button" disabled={pairLoading} onClick={() => void createPairing()}>
                        <RefreshCw data-icon="inline-start" />
                        {pairLoading ? 'Generating…' : 'Generate new code'}
                      </Button>
                      <Button type="button" variant="outline" onClick={onNavigateDownloads}>
                        Download Android app <ExternalLink data-icon="inline-end" />
                      </Button>
                    </div>
                  </div>
                </div>
              )}
            </TabsContent>
          )}
          {supportsWifiAdb && (
            <TabsContent value="wifi-adb" className="mt-5 grid gap-4">
              {adbError && (
                <Alert variant="destructive">
                  <AlertTitle>ADB connection issue</AlertTitle>
                  <AlertDescription>{adbError}</AlertDescription>
                </Alert>
              )}
              {adbNotice && (
                <Alert>
                  <AlertTitle>Connection updated</AlertTitle>
                  <AlertDescription>{adbNotice}</AlertDescription>
                </Alert>
              )}
              {adbDiscovery && !adbDiscovery.adb_available && (
                <Alert variant="destructive">
                  <AlertTitle>Android platform-tools not found</AlertTitle>
                  <AlertDescription>
                    Install Android SDK Platform Tools so the console can use USB or Wi-Fi ADB. USB-connected
                    phones appear automatically after ADB is available.
                  </AlertDescription>
                </Alert>
              )}
              <Card>
                <CardHeader className="grid-cols-[minmax(0,1fr)_auto] items-start justify-between gap-4">
                  <div className="grid gap-1">
                    <CardTitle className="text-base">Find phones on your Wi-Fi</CardTitle>
                    <CardDescription>
                      On Android, open Developer options → Wireless debugging.
                    </CardDescription>
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    disabled={adbScanning}
                    onClick={() => void scanWifiAdb()}
                  >
                    <RefreshCw data-icon="inline-start" />
                    {adbScanning ? 'Scanning…' : 'Scan'}
                  </Button>
                </CardHeader>
                <CardContent className="grid gap-2">
                  {adbDiscovery?.wireless_services.length ? (
                    adbDiscovery.wireless_services.map((svc) => (
                      <div
                        key={`${svc.type}-${svc.address}`}
                        className="flex flex-col gap-3 rounded-lg border p-3 sm:flex-row sm:items-center sm:justify-between"
                      >
                        <div className="grid gap-1">
                          <span className="font-medium">
                            {svc.type === 'pairing' ? 'Pairing endpoint' : 'Ready to connect'}
                          </span>
                          <span className="text-sm text-muted-foreground">
                            <code>{svc.address}</code> · {svc.name}
                          </span>
                        </div>
                        {svc.type === 'pairing' ? (
                          <Button variant="outline" onClick={() => setPairAddr(svc.address)}>
                            Use pairing address
                          </Button>
                        ) : (
                          <Button disabled={adbBusy} onClick={() => void submitWifiConnect(svc.address)}>
                            Connect phone
                          </Button>
                        )}
                      </div>
                    ))
                  ) : (
                    <p className="text-sm text-muted-foreground">
                      {adbScanning
                        ? 'Searching for Android devices…'
                        : 'No devices discovered yet. Enter the Wireless debugging addresses below.'}
                    </p>
                  )}
                </CardContent>
              </Card>
              <div className="grid gap-4 lg:grid-cols-2">
                <Card>
                  <CardHeader>
                    <CardTitle className="text-base">1. Pair this computer</CardTitle>
                    <CardDescription>On the phone, choose “Pair device with pairing code”.</CardDescription>
                  </CardHeader>
                  <CardContent>
                    <form onSubmit={submitWifiPair} className="grid gap-4">
                      <FieldGroup className="sm:flex-row">
                        <Field className="sm:flex-1">
                          <FieldLabel>IP address and pairing port</FieldLabel>
                          <Input
                            required
                            placeholder="192.168.1.42:37199"
                            value={pairAddr}
                            onChange={(e) => setPairAddr(e.target.value)}
                          />
                        </Field>
                        <Field className="sm:flex-1">
                          <FieldLabel>6-digit pairing code</FieldLabel>
                          <Input
                            required
                            pattern="[0-9]{6}"
                            maxLength={6}
                            inputMode="numeric"
                            placeholder="123456"
                            value={pairCode}
                            onChange={(e) => setPairCode(e.target.value)}
                          />
                        </Field>
                      </FieldGroup>
                      <Button
                        type="submit"
                        disabled={adbBusy || !pairAddr.trim() || pairCode.trim().length !== 6}
                      >
                        <Cable data-icon="inline-start" />
                        {adbBusy ? 'Pairing…' : 'Pair over Wi-Fi'}
                      </Button>
                    </form>
                  </CardContent>
                </Card>
                <Card>
                  <CardHeader>
                    <CardTitle className="text-base">2. Connect paired phone</CardTitle>
                    <CardDescription>Use the IP address and port on Wireless debugging.</CardDescription>
                  </CardHeader>
                  <CardContent>
                    <form
                      onSubmit={(e) => {
                        e.preventDefault();
                        void submitWifiConnect(connectAddr);
                      }}
                      className="grid gap-4"
                    >
                      <Field>
                        <FieldLabel>IP address and debug port</FieldLabel>
                        <Input
                          required
                          placeholder="192.168.1.42:41235"
                          value={connectAddr}
                          onChange={(e) => setConnectAddr(e.target.value)}
                        />
                      </Field>
                      <Button type="submit" disabled={adbBusy || !connectAddr.trim()}>
                        Connect phone
                      </Button>
                    </form>
                  </CardContent>
                </Card>
              </div>
            </TabsContent>
          )}
          <TabsContent value="remote" className="mt-5 grid gap-4 md:grid-cols-2">
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Remote Android host</CardTitle>
                <CardDescription>
                  Run a connector on the computer that can reach the devices, then connect it to your private
                  relay.
                </CardDescription>
              </CardHeader>
              <CardContent className="grid gap-3">
                <pre className="code-surface overflow-x-auto rounded-lg p-4 text-sm">
                  <code>{`mobile-use connector --endpoint relay.example.internal:9443 \\\n  --workspace ${actor.workspace} --name android-host-1 \\\n  --cert android-host-1.crt --key android-host-1.key --ca relay-ca.crt`}</code>
                </pre>
                <p className="text-sm text-muted-foreground">
                  Connector packages and deployment guidance are available in Downloads.
                </p>
              </CardContent>
            </Card>
            <Card>
              <CardHeader>
                <CardTitle className="text-base">Remote server from this computer</CardTitle>
                <CardDescription>
                  Pair an Android companion over HTTPS or configure the local CLI for your workspace.
                </CardDescription>
              </CardHeader>
              <CardContent className="grid gap-3">
                <pre className="code-surface overflow-x-auto rounded-lg p-4 text-sm">
                  <code>{remoteSetup}</code>
                </pre>
                <p className="text-sm text-muted-foreground">
                  On macOS, iPhone support uses Appium and WebDriverAgent through a local connector.
                </p>
              </CardContent>
            </Card>
          </TabsContent>
        </Tabs>
      </CardContent>
    </Card>
  );
}
