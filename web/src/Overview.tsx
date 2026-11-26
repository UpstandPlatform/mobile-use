import {
  Activity,
  ArrowRight,
  Bot,
  Check,
  Download,
  MonitorSmartphone,
  Plus,
  Radio,
  Smartphone,
  Wifi,
} from 'lucide-react';
import type { Device, Session, SystemInfo } from './api';
import type { ConsolePage } from './AppSidebar';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import { Progress } from '@/components/ui/progress';

export function Overview({
  devices,
  sessions,
  systemInfo,
  onNavigate,
  onAddDevice,
}: {
  devices: Device[];
  sessions: Session[];
  systemInfo?: SystemInfo;
  onNavigate: (page: ConsolePage) => void;
  onAddDevice: () => void;
}) {
  const onlineDevices = devices.filter((device) => device.online).length;
  const activeSessions = sessions.filter((session) => session.state === 'active').length;
  const pendingSessions = sessions.filter((session) => session.state === 'pending_approval').length;
  const agentReady = devices.filter(
    (device) => device.enabled && device.online && (device.grants?.length || 0) > 0,
  ).length;
  const hasCompanion = devices.some((device) => device.mode === 'companion');
  const hasAgentGrant = devices.some((device) => (device.grants || []).length > 0);
  const completion = [devices.length > 0, activeSessions > 0, hasAgentGrant].filter(Boolean).length;
  const local = systemInfo?.mode !== 'remote';

  const steps = [
    {
      title: 'Connect your phone',
      description: hasCompanion
        ? 'The companion is paired. It reconnects automatically when it can reach this server.'
        : 'Pair the Android companion for screen sharing, or use ADB for full device controls.',
      complete: devices.length > 0,
      icon: Smartphone,
      action: devices.length > 0 ? () => onNavigate('Devices') : onAddDevice,
      actionLabel: devices.length > 0 ? 'View devices' : 'Connect a phone',
    },
    {
      title: 'Start a secure session',
      description:
        activeSessions > 0
          ? `${activeSessions} session${activeSessions === 1 ? ' is' : 's are'} active.`
          : 'Request only the capabilities you need; the phone owner approves each session.',
      complete: activeSessions > 0,
      icon: Activity,
      action: () => onNavigate('Sessions'),
      actionLabel:
        pendingSessions > 0
          ? `Review ${pendingSessions} request${pendingSessions === 1 ? '' : 's'}`
          : 'Open sessions',
    },
    {
      title: 'Connect your agent',
      description: hasAgentGrant
        ? `${agentReady} device${agentReady === 1 ? '' : 's'} ready for granted agent access.`
        : 'Copy the one-line MCP setup for Codex, then grant the agent access on a device.',
      complete: hasAgentGrant,
      icon: Bot,
      action: () => onNavigate('Agent setup'),
      actionLabel: hasAgentGrant ? 'Review agent setup' : 'Set up Codex',
    },
  ];

  return (
    <div className="grid gap-6">
      <Card className="overflow-hidden border-primary/20">
        <div className="grid gap-6 bg-gradient-to-br from-primary/10 via-background to-secondary/50 p-6 md:grid-cols-[minmax(0,1fr)_auto] md:items-center md:p-8">
          <div className="grid max-w-3xl gap-4">
            <div className="flex flex-wrap items-center gap-2">
              <Badge variant="secondary">{local ? 'Local workstation' : 'Remote workspace'}</Badge>
              {systemInfo?.companion_enabled && (
                <Badge variant="outline">
                  <Wifi data-icon="inline-start" /> Wi-Fi companion ready
                </Badge>
              )}
            </div>
            <div className="grid gap-2">
              <h2 className="text-3xl font-semibold tracking-tight sm:text-4xl">
                Your phones, ready for agents.
              </h2>
              <p className="max-w-2xl text-base text-muted-foreground">
                Pair a device, approve a session, and let Codex or another agent work within the access you
                grant.
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button onClick={onAddDevice}>
                {devices.length === 0 ? 'Connect your first phone' : 'Add a phone'}
                <Plus data-icon="inline-start" />
              </Button>
              <Button variant="outline" onClick={() => onNavigate('Downloads')}>
                Downloads and guides <Download data-icon="inline-start" />
              </Button>
            </div>
          </div>
          <div className="hidden size-32 place-items-center rounded-[2rem] bg-primary/10 text-primary md:grid lg:size-40">
            <MonitorSmartphone className="size-16" />
          </div>
        </div>
      </Card>

      {pendingSessions > 0 && (
        <Card className="border-primary/30 bg-primary/5">
          <CardContent className="flex flex-col gap-3 p-4 sm:flex-row sm:items-center sm:justify-between">
            <div className="flex items-start gap-3">
              <span className="grid size-9 shrink-0 place-items-center rounded-lg bg-primary/10 text-primary">
                <Radio />
              </span>
              <div className="grid gap-1">
                <p className="font-medium">Session approval needed</p>
                <p className="text-sm text-muted-foreground">
                  Review who requested access and which phone capabilities they need.
                </p>
              </div>
            </div>
            <Button variant="outline" onClick={() => onNavigate('Sessions')}>
              Review requests <ArrowRight data-icon="inline-end" />
            </Button>
          </CardContent>
        </Card>
      )}

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4" aria-label="Workspace status">
        <StatusCard
          icon={Smartphone}
          label="Devices"
          value={`${onlineDevices} online`}
          detail={`${devices.length} enrolled or awaiting setup`}
        />
        <StatusCard
          icon={Activity}
          label="Sessions"
          value={`${activeSessions} active`}
          detail={pendingSessions ? `${pendingSessions} waiting for approval` : 'Access stays time-limited'}
        />
        <StatusCard
          icon={Bot}
          label="Agent access"
          value={`${agentReady} ready`}
          detail="Online, enrolled, and granted"
        />
        <StatusCard
          icon={Wifi}
          label="Connection"
          value={local ? 'Local + Wi-Fi' : 'Remote HTTPS'}
          detail={systemInfo?.host_name || systemInfo?.origin || 'Connection details loading'}
        />
      </section>

      <Card>
        <CardHeader className="gap-4 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
          <div className="grid gap-1">
            <CardTitle>Get started</CardTitle>
            <CardDescription>
              A short setup path for a new workspace. You can change grants and connection methods later.
            </CardDescription>
          </div>
          <div className="flex items-center gap-3 sm:min-w-40">
            <Progress
              value={(completion / steps.length) * 100}
              aria-label={`${completion} of ${steps.length} setup steps complete`}
            />
            <span className="shrink-0 text-sm text-muted-foreground">
              {completion}/{steps.length}
            </span>
          </div>
        </CardHeader>
        <CardContent className="grid gap-3 lg:grid-cols-3">
          {steps.map((step, index) => (
            <Card key={step.title} className="bg-muted/40">
              <CardHeader className="gap-3">
                <div className="flex items-center justify-between gap-2">
                  <span className="grid size-9 place-items-center rounded-lg bg-background text-primary">
                    <step.icon />
                  </span>
                  <Badge variant={step.complete ? 'secondary' : 'outline'}>
                    {step.complete ? (
                      <>
                        <Check data-icon="inline-start" /> Complete
                      </>
                    ) : (
                      `Step ${index + 1}`
                    )}
                  </Badge>
                </div>
                <div className="grid gap-1">
                  <CardTitle className="text-base">{step.title}</CardTitle>
                  <CardDescription>{step.description}</CardDescription>
                </div>
              </CardHeader>
              <CardContent>
                <Button variant="outline" className="w-full justify-between" onClick={step.action}>
                  {step.actionLabel}
                  <ArrowRight data-icon="inline-end" />
                </Button>
              </CardContent>
            </Card>
          ))}
        </CardContent>
      </Card>
    </div>
  );
}

function StatusCard({
  icon: Icon,
  label,
  value,
  detail,
}: {
  icon: typeof Smartphone;
  label: string;
  value: string;
  detail: string;
}) {
  return (
    <Card>
      <CardHeader className="grid-cols-[minmax(0,1fr)_auto] items-start gap-3 space-y-0">
        <div className="grid gap-1">
          <CardDescription>{label}</CardDescription>
          <CardTitle className="text-2xl">{value}</CardTitle>
        </div>
        <span className="grid size-9 place-items-center rounded-lg bg-secondary text-secondary-foreground">
          <Icon />
        </span>
      </CardHeader>
      <CardContent className="pt-0 text-sm text-muted-foreground">{detail}</CardContent>
    </Card>
  );
}
