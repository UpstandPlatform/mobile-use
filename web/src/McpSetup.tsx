import { useState } from 'react';
import { ArrowRight, Bot, CircleHelp, ExternalLink, Smartphone, Terminal } from 'lucide-react';
import type { Device, SystemInfo } from './api';
import { CopyButton } from './AddDevicePanel';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card';
import {
  Empty,
  EmptyContent,
  EmptyDescription,
  EmptyHeader,
  EmptyMedia,
  EmptyTitle,
} from '@/components/ui/empty';
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from '@/components/ui/table';
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs';

export function McpSetup({
  devices,
  systemInfo,
  onSelectDevice,
  onNavigateDownloads,
  onAddDevice,
}: {
  devices: Device[];
  systemInfo?: SystemInfo;
  onSelectDevice: (deviceId: string) => void;
  onNavigateDownloads?: () => void;
  onAddDevice?: () => void;
}) {
  const [client, setClient] = useState('codex');
  const remote = systemInfo?.mode === 'remote';
  const exec = remote ? 'mobile-use' : systemInfo?.mcp_executable || 'mobile-use';
  const codexCmd = remote
    ? `codex mcp add mobile-use -- ${exec} mcp`
    : systemInfo?.mcp_commands?.codex || `codex mcp add mobile-use -- ${exec} mcp`;
  const claudeCmd = remote
    ? `claude mcp add --transport stdio mobile-use -- ${exec} mcp`
    : systemInfo?.mcp_commands?.claude || `claude mcp add --transport stdio mobile-use -- ${exec} mcp`;
  const httpUrl = systemInfo?.mcp_commands?.http || `${location.origin}/mcp`;
  const remoteSetup = `mobile-use remote set --url ${systemInfo?.origin || location.origin} --token <AGENT_TOKEN>`;
  const desktopJson = JSON.stringify(
    { mcpServers: { 'mobile-use': { command: exec, args: ['mcp'] } } },
    null,
    2,
  );
  const readyDevices = devices.filter(
    (device) => device.enabled && device.online && (device.grants?.length || 0) > 0,
  );

  return (
    <div className="grid gap-6">
      <Card className="overflow-hidden border-primary/20">
        <div className="grid gap-5 bg-gradient-to-br from-primary/10 via-background to-secondary/50 p-6 md:grid-cols-[minmax(0,1fr)_auto] md:items-center md:p-8">
          <div className="grid gap-3">
            <Badge variant="secondary" className="w-fit">
              {remote ? 'Remote workspace' : 'Local workstation'}
            </Badge>
            <div className="grid gap-2">
              <h2 className="text-2xl font-semibold tracking-tight sm:text-3xl">
                Connect your agent to your phones
              </h2>
              <p className="max-w-2xl text-muted-foreground">
                Register mobile-use once, then grant each agent only the device capabilities it needs. Session
                approval stays in your workspace.
              </p>
            </div>
            <div className="flex flex-wrap gap-2">
              <Button variant="outline" onClick={onNavigateDownloads}>
                Download CLI and guides <ExternalLink data-icon="inline-end" />
              </Button>
              <Button
                variant="ghost"
                onClick={() =>
                  window.open(
                    'https://github.com/UpstandPlatform/mobile-use#agent-and-mcp-setup',
                    '_blank',
                    'noopener,noreferrer',
                  )
                }
              >
                Read setup docs
              </Button>
            </div>
          </div>
          <span className="hidden size-20 place-items-center rounded-3xl bg-primary/10 text-primary md:grid">
            <Bot className="size-10" />
          </span>
        </div>
      </Card>

      {remote && (
        <Alert>
          <Terminal />
          <AlertTitle>Remote workspace setup</AlertTitle>
          <AlertDescription className="grid gap-3">
            <span>
              On the workstation where Codex or Claude runs, install the CLI, save a scoped agent token as a
              remote profile, then register the local MCP command below.
            </span>
            <div className="flex flex-col gap-2 sm:flex-row sm:items-center">
              <code className="code-surface min-w-0 flex-1 overflow-x-auto rounded-md p-3">
                {remoteSetup}
              </code>
              <CopyButton value={remoteSetup} />
            </div>
            <span>Keep the token in your CLI profile; don’t paste it into shared project files.</span>
          </AlertDescription>
        </Alert>
      )}

      <section className="grid gap-4 sm:grid-cols-2 xl:grid-cols-4" aria-label="Agent readiness">
        <Readiness
          title="Workspace"
          value={remote ? 'Remote HTTPS' : 'Local workstation'}
          detail={systemInfo?.host_name || systemInfo?.origin || 'Loading connection details'}
        />
        <Readiness
          title="MCP-ready phones"
          value={`${readyDevices.length} of ${devices.length}`}
          detail="Online, enrolled, and granted"
        />
        <Readiness
          title="Android companion"
          value={
            systemInfo?.companion_enabled
              ? systemInfo.companion_mode === 'lan'
                ? 'Local Wi-Fi ready'
                : 'Remote HTTPS ready'
              : 'Not enabled'
          }
          detail={systemInfo?.companion_origin || 'Connect from Devices'}
        />
        <Readiness
          title="ADB controls"
          value={systemInfo?.adb_available ? 'Ready' : remote ? 'Use a connector' : 'Not detected'}
          detail={
            systemInfo?.wireless_adb_supported ? 'USB and Wi-Fi ADB' : 'Requires a workstation connector'
          }
        />
      </section>

      <Card>
        <CardHeader>
          <CardTitle>1. Register your MCP client</CardTitle>
          <CardDescription>
            Choose your agent. The command includes the correct executable for this workspace.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4">
          <Tabs value={client} onValueChange={setClient}>
            <TabsList className="grid h-auto w-full grid-cols-2 sm:grid-cols-4">
              <TabsTrigger value="codex">Codex</TabsTrigger>
              <TabsTrigger value="claude-code">Claude Code</TabsTrigger>
              <TabsTrigger value="desktop">Desktop / Cursor</TabsTrigger>
              <TabsTrigger value="http">HTTP MCP</TabsTrigger>
            </TabsList>
            <TabsContent value="codex" className="mt-4">
              <CommandCard
                title="Codex CLI"
                description="Run this in a terminal where Codex CLI is installed."
                value={codexCmd}
              />
            </TabsContent>
            <TabsContent value="claude-code" className="mt-4">
              <CommandCard
                title="Claude Code"
                description="Run this in the terminal where Claude Code is installed."
                value={claudeCmd}
              />
            </TabsContent>
            <TabsContent value="desktop" className="mt-4">
              <CommandCard
                title="Claude Desktop or Cursor"
                description="Add this server entry to the client’s MCP configuration file."
                value={desktopJson}
                copyLabel="Copy JSON"
              />
            </TabsContent>
            <TabsContent value="http" className="mt-4">
              <CommandCard
                title="Streamable HTTP MCP"
                description="For networked MCP clients that connect with a bearer agent token."
                value={httpUrl}
              />
              <p className="mt-3 text-sm text-muted-foreground">
                The CLI can show the current endpoint and setup details with{' '}
                <code>mobile-use mcp-config</code>.
              </p>
            </TabsContent>
          </Tabs>
          <div className="grid gap-3 rounded-lg border bg-muted/30 p-4 sm:grid-cols-3">
            <SetupStep
              number="2"
              title="Connect a phone"
              text="Use the companion QR flow, Wi-Fi ADB, or a remote connector."
            />
            <SetupStep
              number="3"
              title="Grant the agent"
              text="Enroll a phone and allow the agent identity the capabilities it needs."
            />
            <SetupStep
              number="4"
              title="Approve a session"
              text="Review the request on the device or console before control starts."
            />
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardHeader>
          <CardTitle>Device agent access</CardTitle>
          <CardDescription>
            Only online, enrolled phones with a grant are available to agent tools.
          </CardDescription>
        </CardHeader>
        <CardContent>
          {devices.length === 0 ? (
            <Empty>
              <EmptyHeader>
                <EmptyMedia variant="icon">
                  <Smartphone />
                </EmptyMedia>
                <EmptyTitle>No devices connected</EmptyTitle>
                <EmptyDescription>Add a phone to start granting agent access.</EmptyDescription>
              </EmptyHeader>
              <EmptyContent>
                <Button onClick={onAddDevice}>Connect a device</Button>
                <Button variant="outline" onClick={onNavigateDownloads}>
                  Open setup guides
                </Button>
              </EmptyContent>
            </Empty>
          ) : (
            <div className="overflow-x-auto rounded-md border">
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead>Device</TableHead>
                    <TableHead>Transport</TableHead>
                    <TableHead>Status</TableHead>
                    <TableHead>Agent grants</TableHead>
                    <TableHead>
                      <span className="sr-only">Action</span>
                    </TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {devices.map((device) => (
                    <TableRow key={device.id}>
                      <TableCell className="font-medium">{device.label}</TableCell>
                      <TableCell>
                        {device.platform === 'ios' ? 'iOS' : 'Android'} · {device.mode}
                      </TableCell>
                      <TableCell>
                        <Badge variant={device.online && device.enabled ? 'secondary' : 'outline'}>
                          {device.online ? 'Online' : 'Offline'} ·{' '}
                          {device.enabled ? device.profile : 'Not enrolled'}
                        </Badge>
                      </TableCell>
                      <TableCell>
                        {device.grants?.length ? (
                          device.grants.map((grant) => grant.subject).join(', ')
                        ) : (
                          <span className="text-muted-foreground">No grants</span>
                        )}
                      </TableCell>
                      <TableCell>
                        <Button variant="outline" size="sm" onClick={() => onSelectDevice(device.id)}>
                          Configure
                        </Button>
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            </div>
          )}
        </CardContent>
      </Card>

      <Alert>
        <CircleHelp />
        <AlertTitle>What can an agent do?</AlertTitle>
        <AlertDescription>
          In Codex, once the <code>mobile-use</code> MCP server is connected, ask normally—for example, “Use
          mobile-use to open Instagram on my phone.” The agent can find the paired device, request the Apps
          capability, wait for your approval on Android, match the app name to its package ID, and open it.
          Each action still requires an approved session.
          <br /><br />
          The Android companion lists and opens apps after pairing. Enable its mobile-use accessibility service
          in Android Settings to add the UI tree, touch gestures, text input, navigation, and browser controls
          over local Wi-Fi or remote HTTPS. Screen capture is approved separately on the phone. The phone must
          be unlocked for screen reading and interaction. Browser inspection uses the visible accessibility tree,
          not full DOM access. App force-stop, arbitrary APK installation, and app-data reset use the ADB
          connector. iPhone features require the configured Appium/WebDriverAgent connector.
        </AlertDescription>
      </Alert>
    </div>
  );
}

function CommandCard({
  title,
  description,
  value,
  copyLabel = 'Copy command',
}: {
  title: string;
  description: string;
  value: string;
  copyLabel?: string;
}) {
  return (
    <Card className="bg-muted/25">
      <CardHeader className="gap-1 pb-3">
        <CardTitle className="text-base">{title}</CardTitle>
        <CardDescription>{description}</CardDescription>
      </CardHeader>
      <CardContent className="grid gap-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-start">
        <pre className="code-surface min-w-0 overflow-x-auto rounded-md p-3 text-sm">
          <code>{value}</code>
        </pre>
        <CopyButton value={value} label={copyLabel} />
      </CardContent>
    </Card>
  );
}

function Readiness({ title, value, detail }: { title: string; value: string; detail: string }) {
  return (
    <Card>
      <CardHeader className="gap-1">
        <CardDescription>{title}</CardDescription>
        <CardTitle className="text-lg">{value}</CardTitle>
      </CardHeader>
      <CardContent className="break-all pt-0 text-sm text-muted-foreground">{detail}</CardContent>
    </Card>
  );
}

function SetupStep({ number, title, text }: { number: string; title: string; text: string }) {
  return (
    <div className="flex gap-3">
      <Badge variant="outline" className="size-7 shrink-0 justify-center rounded-full">
        {number}
      </Badge>
      <div className="grid gap-1">
        <span className="text-sm font-medium">{title}</span>
        <span className="text-sm text-muted-foreground">{text}</span>
      </div>
      <ArrowRight className="ml-auto size-4 shrink-0 text-muted-foreground" />
    </div>
  );
}
