import { useEffect, useMemo, useState } from 'react';
import {
  ArrowDownToLine,
  ArrowUpRight,
  Check,
  CircleHelp,
  Code2,
  Laptop,
  PackageCheck,
  ShieldCheck,
  Smartphone,
  Terminal,
  Wifi,
} from 'lucide-react';
import type { SystemInfo } from './api';
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert';
import { Accordion, AccordionContent, AccordionItem, AccordionTrigger } from '@/components/ui/accordion';
import { Badge } from '@/components/ui/badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardDescription, CardFooter, CardHeader, CardTitle } from '@/components/ui/card';
import { Separator } from '@/components/ui/separator';
import { Skeleton } from '@/components/ui/skeleton';

const repository = 'UpstandPlatform/mobile-use';
const releasesUrl = `https://github.com/${repository}/releases`;

type ReleaseAsset = {
  name: string;
  size: number;
  browser_download_url: string;
};

type Release = {
  tag_name: string;
  html_url: string;
  published_at?: string;
  assets: ReleaseAsset[];
};

type Artifact = {
  name: string;
  title: string;
  detail: string;
  size?: number;
};

function formatBytes(size?: number) {
  if (!size) return '';
  const units = ['B', 'KB', 'MB', 'GB'];
  let value = size;
  let unit = 0;
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return `${value >= 10 || unit === 0 ? value.toFixed(0) : value.toFixed(1)} ${units[unit]}`;
}

function ReleaseDownload({
  artifact,
  href,
  unavailable = false,
}: {
  artifact: Artifact;
  href?: string;
  unavailable?: boolean;
}) {
  return (
    <div className="flex flex-col gap-3 rounded-xl border bg-card p-4 sm:flex-row sm:items-center sm:justify-between">
      <div className="min-w-0">
        <p className="font-medium">{artifact.title}</p>
        <p className="text-sm text-muted-foreground">{artifact.detail}</p>
        <p className="mt-1 truncate font-mono text-xs text-muted-foreground">
          {artifact.name}
          {artifact.size ? ` · ${formatBytes(artifact.size)}` : ''}
        </p>
      </div>
      <Button
        className="shrink-0"
        variant={unavailable ? 'outline' : 'secondary'}
        render={
          <a
            href={href || releasesUrl}
            target="_blank"
            rel="noreferrer"
            aria-label={`Download ${artifact.title}`}
          />
        }
      >
        {unavailable ? 'Browse releases' : 'Download'}
        {unavailable ? <ArrowUpRight data-icon="inline-end" /> : <ArrowDownToLine data-icon="inline-end" />}
      </Button>
    </div>
  );
}

export function Downloads({ systemInfo }: { systemInfo?: SystemInfo }) {
  const [release, setRelease] = useState<Release>();
  const [releaseUnavailable, setReleaseUnavailable] = useState(false);
  const serverTag = systemInfo?.version?.startsWith('v') ? systemInfo.version : undefined;

  useEffect(() => {
    const controller = new AbortController();
    fetch(`https://api.github.com/repos/${repository}/releases/latest`, {
      headers: { Accept: 'application/vnd.github+json' },
      signal: controller.signal,
    })
      .then(async (response) => {
        if (!response.ok) throw new Error(`Release lookup failed (${response.status})`);
        const latest = (await response.json()) as Release;
        if (!latest.tag_name || !Array.isArray(latest.assets))
          throw new Error('Release metadata is incomplete.');
        setRelease(latest);
      })
      .catch((error) => {
        if (error instanceof DOMException && error.name === 'AbortError') return;
        setReleaseUnavailable(true);
      });
    return () => controller.abort();
  }, []);

  const tag = release?.tag_name || serverTag;
  const version = tag?.replace(/^v/, '');
  const artifactTag = tag || '<version>';
  const releaseHref =
    release?.html_url || (tag ? `${releasesUrl}/tag/${encodeURIComponent(tag)}` : `${releasesUrl}/latest`);
  const assets = useMemo(
    () => new Map((release?.assets || []).map((asset) => [asset.name, asset])),
    [release],
  );
  const asset = (name: string): ReleaseAsset | undefined => assets.get(name);
  const directUrl = (name: string) =>
    asset(name)?.browser_download_url ||
    (tag
      ? `https://github.com/${repository}/releases/download/${encodeURIComponent(tag)}/${encodeURIComponent(name)}`
      : undefined);
  const desktop = [
    {
      name: `mobile-use-${artifactTag}-windows-amd64.zip`,
      title: 'Windows · 64-bit',
      detail: 'CLI and embedded desktop console',
    },
    {
      name: `mobile-use-${artifactTag}-windows-arm64.zip`,
      title: 'Windows · ARM64',
      detail: 'CLI and embedded desktop console',
    },
    {
      name: `mobile-use-${artifactTag}-darwin-arm64.tar.gz`,
      title: 'macOS · Apple silicon',
      detail: 'CLI and embedded desktop console',
    },
    {
      name: `mobile-use-${artifactTag}-darwin-amd64.tar.gz`,
      title: 'macOS · Intel',
      detail: 'CLI and embedded desktop console',
    },
    {
      name: `mobile-use-${artifactTag}-linux-amd64.tar.gz`,
      title: 'Linux · x86_64',
      detail: 'CLI and embedded desktop console',
    },
    {
      name: `mobile-use-${artifactTag}-linux-arm64.tar.gz`,
      title: 'Linux · ARM64',
      detail: 'CLI and embedded desktop console',
    },
  ];
  const companionName = tag ? `mobile-use-companion-${tag}.apk` : 'mobile-use-companion-<version>.apk';
  const controlName = tag ? `mobile-use-control-${tag}.apk` : 'mobile-use-control-<version>.apk';
  const sdkName = `mobile-use-${artifactTag}-agent-sdks.tar.gz`;
  const deployName = `mobile-use-${artifactTag}-deploy.tar.gz`;
  const wheelName = version
    ? `mobile_use_client-${version}-py3-none-any.whl`
    : 'mobile_use_client-<version>-py3-none-any.whl';
  const npmName = `mobile-use-${artifactTag}.tgz`;
  const ociAmd64Name = `mobile-use-${artifactTag}-oci-linux-amd64.tar.gz`;
  const ociArm64Name = `mobile-use-${artifactTag}-oci-linux-arm64.tar.gz`;
  const companion = asset(companionName);
  const control = asset(controlName);

  return (
    <div className="grid gap-8">
      <div className="flex flex-col gap-4 md:flex-row md:items-end md:justify-between">
        <div className="grid gap-2">
          <div className="flex flex-wrap items-center gap-2">
            <Badge variant="secondary">Downloads &amp; setup</Badge>
            {tag && <Badge variant="outline">{release ? `Latest ${tag}` : `Server build ${tag}`}</Badge>}
          </div>
          <h2 className="text-3xl font-semibold tracking-tight">Everything you need to connect</h2>
          <p className="max-w-3xl text-muted-foreground">
            Get the Android app, desktop tools, and setup guides in one place. Download links open the
            matching GitHub release; this private project may ask you to sign in with an account that has
            access.
          </p>
        </div>
        <Button variant="outline" render={<a href={releaseHref} target="_blank" rel="noreferrer" />}>
          {release ? 'Release notes' : 'All releases'}
          <ArrowUpRight data-icon="inline-end" />
        </Button>
      </div>

      {!tag && !release && (
        <Alert>
          <CircleHelp />
          <AlertTitle>Release metadata is not available yet</AlertTitle>
          <AlertDescription>
            Browse GitHub releases to choose a version. You can still follow the setup guides below.
          </AlertDescription>
        </Alert>
      )}

      <Alert>
        <CircleHelp />
        <AlertTitle>GitHub access may be required</AlertTitle>
        <AlertDescription>
          Artifacts are hosted on the project’s GitHub releases. If a download asks you to sign in, use an
          account with access to this repository. The console never sends your mobile-use admin or agent token
          to GitHub.
        </AlertDescription>
      </Alert>

      <section
        className="grid gap-4 xl:grid-cols-[minmax(0,1.2fr)_minmax(18rem,0.8fr)]"
        aria-label="Recommended downloads"
      >
        <Card className="overflow-hidden border-primary/20">
          <CardHeader className="bg-primary/5">
            <div className="flex items-center gap-3">
              <span className="grid size-11 place-items-center rounded-xl bg-primary/10 text-primary">
                <Smartphone />
              </span>
              <div className="grid gap-1">
                <CardTitle>Android companion</CardTitle>
                <CardDescription>
                  Start here for screen sharing and approved camera or microphone requests.
                </CardDescription>
              </div>
            </div>
          </CardHeader>
          <CardContent className="grid gap-4 pt-5">
            <p className="text-sm text-muted-foreground">
              Install it on your phone, then scan the pairing QR from{' '}
              <strong className="text-foreground">Devices → Add phone</strong>. Keep the phone and local
              workstation on the same Wi-Fi network for automatic discovery.
            </p>
            <ReleaseDownload
              artifact={{
                name: companionName,
                title: 'Download Android companion APK',
                detail: 'Required for phone screen sharing and sensors',
                size: companion?.size,
              }}
              href={directUrl(companionName)}
              unavailable={!directUrl(companionName)}
            />
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-center gap-3">
              <span className="grid size-11 place-items-center rounded-xl bg-secondary text-secondary-foreground">
                <Laptop />
              </span>
              <div className="grid gap-1">
                <CardTitle>Workstation CLI</CardTitle>
                <CardDescription>The console, local daemon, pairing, and MCP host.</CardDescription>
              </div>
            </div>
          </CardHeader>
          <CardContent className="grid gap-3">
            {desktop.slice(0, 3).map((item) => (
              <ReleaseDownload
                key={item.name}
                artifact={{ ...item, size: asset(item.name)?.size }}
                href={directUrl(item.name)}
                unavailable={!directUrl(item.name)}
              />
            ))}
            <p className="text-xs text-muted-foreground">
              Need a different platform or CPU? Expand the desktop packages below.
            </p>
          </CardContent>
        </Card>
      </section>

      <section className="grid gap-4 xl:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]" aria-label="Guided setup">
        <Card>
          <CardHeader>
            <div className="flex items-center gap-3">
              <span className="grid size-10 place-items-center rounded-xl bg-secondary text-secondary-foreground">
                <Wifi />
              </span>
              <div className="grid gap-1">
                <CardTitle>First phone setup</CardTitle>
                <CardDescription>
                  Pair once; the companion reconnects automatically afterward.
                </CardDescription>
              </div>
            </div>
          </CardHeader>
          <CardContent>
            <Accordion>
              <AccordionItem value="pair-local">
                <AccordionTrigger>Pair over local Wi-Fi</AccordionTrigger>
                <AccordionContent>
                  <ol className="grid list-decimal gap-2 pl-5 text-sm text-muted-foreground">
                    <li>Download and open the companion on Android.</li>
                    <li>
                      In the console, choose <strong className="text-foreground">Devices → Add phone</strong>.
                      The QR is created automatically.
                    </li>
                    <li>
                      Scan the QR, confirm the workstation name on the phone, and keep both devices on the
                      same Wi-Fi.
                    </li>
                    <li>
                      Request a session in the console, approve it on the phone, then choose{' '}
                      <strong className="text-foreground">Share screen</strong> and accept Android’s capture
                      prompt.
                    </li>
                  </ol>
                </AccordionContent>
              </AccordionItem>
              <AccordionItem value="pair-remote">
                <AccordionTrigger>Pair with a remote server</AccordionTrigger>
                <AccordionContent>
                  <p className="text-sm text-muted-foreground">
                    Open the remote server’s console, create a companion pairing QR, and scan it from Android.
                    Remote pairing requires a reachable HTTPS origin; use{' '}
                    <strong className="text-foreground">Agent setup</strong> for workstation and connector
                    options.
                  </p>
                </AccordionContent>
              </AccordionItem>
              <AccordionItem value="adb-control">
                <AccordionTrigger>Enable app, keyboard, and navigation controls</AccordionTrigger>
                <AccordionContent>
                  <div className="grid gap-3 text-sm text-muted-foreground">
                    <p>
                      These controls use authorized USB or Wireless Debugging through ADB. The companion app
                      handles screen sharing and sensor approvals; it does not provide device input.
                    </p>
                    <ol className="grid list-decimal gap-2 pl-5">
                      <li>Enable Android Developer options and Wireless debugging.</li>
                      <li>
                        Choose{' '}
                        <strong className="text-foreground">
                          Devices → Add device → Android Wi-Fi (ADB)
                        </strong>
                        , then pair and connect the phone.
                      </li>
                      <li>Enroll the device and grant the agent only the capabilities it needs.</li>
                    </ol>
                  </div>
                </AccordionContent>
              </AccordionItem>
            </Accordion>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <div className="flex items-center gap-3">
              <span className="grid size-10 place-items-center rounded-xl bg-secondary text-secondary-foreground">
                <Code2 />
              </span>
              <div className="grid gap-1">
                <CardTitle>Connect Codex and other agents</CardTitle>
                <CardDescription>Local workstations use the CLI as a stdio MCP server.</CardDescription>
              </div>
            </div>
          </CardHeader>
          <CardContent className="grid gap-4">
            <ol className="grid list-decimal gap-2 pl-5 text-sm text-muted-foreground">
              <li>Install the CLI package for your workstation.</li>
              <li>
                Open <strong className="text-foreground">Agent setup</strong> and copy the command for Codex,
                Claude Code, or another client.
              </li>
              <li>
                Restart the agent client. Grant device access from{' '}
                <strong className="text-foreground">Devices</strong> before asking it to act.
              </li>
            </ol>
            <Alert>
              <ShieldCheck />
              <AlertTitle>Device access stays approval-based</AlertTitle>
              <AlertDescription>
                Personal devices require session approval. App install and reset also require a dedicated
                profile, a catalog entry, and explicit confirmation.
              </AlertDescription>
            </Alert>
          </CardContent>
          <CardFooter>
            <Button
              variant="outline"
              render={
                <a
                  href="https://github.com/UpstandPlatform/mobile-use/blob/main/README.md"
                  target="_blank"
                  rel="noreferrer"
                />
              }
            >
              Read the quick start <ArrowUpRight data-icon="inline-end" />
            </Button>
          </CardFooter>
        </Card>
      </section>

      <section className="grid gap-4" aria-label="Additional artifacts">
        <div className="grid gap-2">
          <h3 className="text-xl font-semibold tracking-tight">More downloads</h3>
          <p className="text-sm text-muted-foreground">
            Optional packages for direct Android control, SDK integrations, and self-hosted deployments.
          </p>
        </div>
        <div className="grid gap-4 xl:grid-cols-2">
          <Card>
            <CardHeader>
              <div className="flex items-center gap-3">
                <span className="grid size-10 place-items-center rounded-xl bg-secondary text-secondary-foreground">
                  <PackageCheck />
                </span>
                <div className="grid gap-1">
                  <CardTitle>Android and agent packages</CardTitle>
                  <CardDescription>Use only the optional package your workflow needs.</CardDescription>
                </div>
              </div>
            </CardHeader>
            <CardContent className="grid gap-3">
              <ReleaseDownload
                artifact={{
                  name: controlName,
                  title: 'Android control helper APK',
                  detail: 'Optional ADB helper for Unicode text and pinch gestures',
                  size: control?.size,
                }}
                href={directUrl(controlName)}
                unavailable={!directUrl(controlName)}
              />
              <ReleaseDownload
                artifact={{
                  name: sdkName,
                  title: 'Agent SDK bundle',
                  detail: 'TypeScript and Python SDK sources with API schema',
                  size: asset(sdkName)?.size,
                }}
                href={directUrl(sdkName)}
                unavailable={!directUrl(sdkName)}
              />
              <ReleaseDownload
                artifact={{
                  name: wheelName,
                  title: 'Python SDK wheel',
                  detail: 'Install into a Python environment with pip',
                  size: asset(wheelName)?.size,
                }}
                href={directUrl(wheelName)}
                unavailable={!directUrl(wheelName)}
              />
              <ReleaseDownload
                artifact={{
                  name: npmName,
                  title: 'CLI package archive',
                  detail: 'Package-manager artifact for the command-line client',
                  size: asset(npmName)?.size,
                }}
                href={directUrl(npmName)}
                unavailable={!directUrl(npmName)}
              />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <div className="flex items-center gap-3">
                <span className="grid size-10 place-items-center rounded-xl bg-secondary text-secondary-foreground">
                  <Terminal />
                </span>
                <div className="grid gap-1">
                  <CardTitle>Self-hosting and verification</CardTitle>
                  <CardDescription>Deployment files, OCI images, and checksums.</CardDescription>
                </div>
              </div>
            </CardHeader>
            <CardContent className="grid gap-3">
              <ReleaseDownload
                artifact={{
                  name: deployName,
                  title: 'Deployment bundle',
                  detail: 'Compose, Helm, and operational documentation',
                  size: asset(deployName)?.size,
                }}
                href={directUrl(deployName)}
                unavailable={!directUrl(deployName)}
              />
              <ReleaseDownload
                artifact={{
                  name: ociAmd64Name,
                  title: 'OCI image · Linux x86_64',
                  detail: 'Offline container image archive for amd64 hosts',
                  size: asset(ociAmd64Name)?.size,
                }}
                href={directUrl(ociAmd64Name)}
                unavailable={!directUrl(ociAmd64Name)}
              />
              <ReleaseDownload
                artifact={{
                  name: ociArm64Name,
                  title: 'OCI image · Linux ARM64',
                  detail: 'Offline container image archive for arm64 hosts',
                  size: asset(ociArm64Name)?.size,
                }}
                href={directUrl(ociArm64Name)}
                unavailable={!directUrl(ociArm64Name)}
              />
              <ReleaseDownload
                artifact={{
                  name: 'oci-image.txt',
                  title: 'OCI image reference',
                  detail: 'Published registry image and tag',
                  size: asset('oci-image.txt')?.size,
                }}
                href={directUrl('oci-image.txt')}
                unavailable={!directUrl('oci-image.txt')}
              />
              <ReleaseDownload
                artifact={{
                  name: 'SHA256SUMS',
                  title: 'Release checksums',
                  detail: 'Verify downloaded artifacts before installing',
                  size: asset('SHA256SUMS')?.size,
                }}
                href={directUrl('SHA256SUMS')}
                unavailable={!directUrl('SHA256SUMS')}
              />
              <Separator />
              <div className="flex flex-col gap-2 text-sm text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
                <span>Optional WebRTC screen server</span>
                <Button
                  variant="link"
                  className="h-auto justify-start px-0"
                  render={
                    <a
                      href="https://github.com/Genymobile/scrcpy/releases/latest"
                      target="_blank"
                      rel="noreferrer"
                    />
                  }
                >
                  Latest scrcpy release <ArrowUpRight data-icon="inline-end" />
                </Button>
              </div>
              <Alert>
                <ShieldCheck />
                <AlertTitle>Check signatures and checksums</AlertTitle>
                <AlertDescription>
                  Release APKs are signed by the project release key. The control helper and scrcpy server are
                  optional; the companion APK is enough for the standard Wi-Fi screen-sharing flow.
                </AlertDescription>
              </Alert>
            </CardContent>
          </Card>
        </div>
      </section>

      <Card>
        <CardHeader>
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="grid gap-1">
              <CardTitle>All desktop packages</CardTitle>
              <CardDescription>
                Pick the package that matches the computer running the CLI or connector.
              </CardDescription>
            </div>
            {release ? (
              <Badge variant="secondary">
                <Check data-icon="inline-start" /> Verified release metadata
              </Badge>
            ) : releaseUnavailable ? (
              <Badge variant="outline">Using server version</Badge>
            ) : (
              <Skeleton className="h-5 w-28" />
            )}
          </div>
        </CardHeader>
        <CardContent className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
          {desktop.map((item) => (
            <ReleaseDownload
              key={item.name}
              artifact={{ ...item, size: asset(item.name)?.size }}
              href={directUrl(item.name)}
              unavailable={!directUrl(item.name)}
            />
          ))}
        </CardContent>
        <CardFooter className="flex flex-col items-start gap-2 text-sm text-muted-foreground sm:flex-row sm:items-center sm:justify-between">
          <span>
            {tag
              ? `Release ${tag}${release?.published_at ? ` · published ${new Date(release.published_at).toLocaleDateString()}` : ''}`
              : 'Choose a version from GitHub releases.'}
          </span>
          <Button
            variant="ghost"
            render={<a href={`${releasesUrl}/latest`} target="_blank" rel="noreferrer" />}
          >
            Browse every artifact <ArrowUpRight data-icon="inline-end" />
          </Button>
        </CardFooter>
      </Card>
    </div>
  );
}
