import { ArrowUpRight, ArrowRight, CodeXml, Radio, ShieldCheck, Smartphone } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { buttonVariants } from "@/components/ui/button"
import { Faq } from "@/components/faq"
import { HeroScene } from "@/components/hero-scene"
import { SiteNav } from "@/components/site-nav"
import { WorkflowStory } from "@/components/workflow-story"
import { cn } from "@/lib/utils"

const repo = "https://github.com/UpstandPlatform/mobile-use"
const releases = `${repo}/releases/latest`
const guide = `${repo}/blob/main/README.md`
const operations = `${repo}/blob/main/docs/operations.md`
const verification = `${repo}/blob/main/docs/verification.md`

const capabilities = [
  {
    index: "01",
    title: "Android over USB or Wi-Fi",
    description: "Use Android Debug Bridge for app discovery, UI inspection, touch, keyboard, navigation, and authorized APK workflows.",
    detail: "ADB connector",
    className: "capability-card capability-card--wide",
    visual: "android",
  },
  {
    index: "02",
    title: "A live view from the companion",
    description: "Pair with a QR code or nearby Wi-Fi. The phone owner approves screen capture and sensor requests on the device.",
    detail: "Android companion",
    className: "capability-card capability-card--tall",
    visual: "companion",
  },
  {
    index: "03",
    title: "iPhone through a Mac connector",
    description: "Connect devices configured for Xcode, Appium, and WebDriverAgent to the same session and MCP flow.",
    detail: "macOS + Appium",
    className: "capability-card capability-card--tall",
    visual: "iphone",
  },
  {
    index: "04",
    title: "Local first. Remote when needed.",
    description: "Keep the service on your workstation, or use your own HTTPS deployment with a remote device connector.",
    detail: "Private deployment",
    className: "capability-card capability-card--wide",
    visual: "remote",
  },
]

export default function Home() {
  return (
    <main>
      <SiteNav />

      <HeroScene releases={releases} />

      <section className="signal-strip" aria-label="Product principles">
        <div className="page-width signal-strip__inner">
          <span className="signal-item"><Radio aria-hidden="true" /> Local first</span>
          <Separator orientation="vertical" className="signal-divider" />
          <span className="signal-item"><Smartphone aria-hidden="true" /> Real devices</span>
          <Separator orientation="vertical" className="signal-divider" />
          <span className="signal-item"><ShieldCheck aria-hidden="true" /> Owner controlled</span>
          <span className="signal-strip__aside">Works with MCP compatible agents</span>
        </div>
      </section>

      <WorkflowStory />

      <section id="capabilities" className="capabilities section-pad">
        <div className="page-width">
          <div className="section-heading section-heading--split">
            <div>
              <p className="eyebrow">One control plane</p>
              <h2>Different devices.<br /><span>One clear path in.</span></h2>
            </div>
            <p className="section-intro">Start with the connection your device supports. Keep approvals, sessions, and agent access in the same operator console.</p>
          </div>

          <div className="capability-grid">
            {capabilities.map((capability) => (
              <Card key={capability.index} className={cn("capability-card-shell", capability.className)}>
                <CardHeader className="capability-card__header">
                  <div className="capability-card__meta">
                    <span className="mono-index">{capability.index}</span>
                    <Badge variant="outline">{capability.detail}</Badge>
                  </div>
                  <CardTitle>{capability.title}</CardTitle>
                  <CardDescription>{capability.description}</CardDescription>
                </CardHeader>
                <CardContent>
                  <CapabilityVisual kind={capability.visual} />
                </CardContent>
              </Card>
            ))}
          </div>
        </div>
      </section>

      <section className="access-section section-pad">
        <div className="page-width access-layout">
          <div className="access-copy">
            <p className="eyebrow">A person stays in control</p>
            <h2>Automation with<br /><span>the right boundaries.</span></h2>
            <p className="section-intro">A device is not an open terminal. Access starts with a session, stays inside its grants, and can be paused or ended by the owner.</p>
            <a className="text-link" href={`${guide}#connect-an-agent-codex-claude-cursor`}>
              Read how access works <ArrowUpRight aria-hidden="true" />
            </a>
          </div>

          <div className="access-flow" aria-label="Device access flow">
            <div className="access-flow__line" aria-hidden="true"><span /></div>
            <AccessStep number="01" title="Request" description="An agent asks for a device session and a defined set of capabilities." status="Waiting for owner" />
            <AccessStep number="02" title="Approve" description="The owner reviews the request in the console or companion app." status="Owner decides" />
            <AccessStep number="03" title="Act & review" description="Actions use the approved session. Pause, revoke, and audit at any time." status="Session is visible" />
          </div>
        </div>
      </section>

      <section id="start" className="start-section section-pad">
        <div className="page-width start-shell">
          <div className="start-heading">
            <div>
              <p className="eyebrow">Start on your workstation</p>
              <h2>From zero to connected<br /><span>in three moves.</span></h2>
            </div>
            <p className="section-intro">The desktop app bundles the service and console. Add the companion only when you need screen sharing or approved sensors.</p>
          </div>

          <div className="start-grid">
            <StartStep number="01" title="Install the desktop app">
              <p>Choose the Windows, macOS, or Linux archive. Keep the extracted folder together.</p>
              <code>mobile-use up</code>
            </StartStep>
            <StartStep number="02" title="Connect a device">
              <p>Use USB or Wireless Debugging for control, or install the companion and pair with QR.</p>
              <a href={releases}>Get the companion APK <ArrowUpRight aria-hidden="true" /></a>
            </StartStep>
            <StartStep number="03" title="Connect your agent">
              <p>Copy the exact MCP setup command, then grant the device access you intend to use.</p>
              <code>mobile-use mcp-config</code>
            </StartStep>
          </div>

          <div className="start-footer">
            <span>Need a guided setup?</span>
            <a className="text-link" href={guide}>Open the quickstart <ArrowRight aria-hidden="true" /></a>
          </div>
        </div>
      </section>

      <section id="faq" className="faq-section section-pad">
        <div className="page-width faq-layout">
          <div>
            <p className="eyebrow">Before you connect</p>
            <h2>Good to know<br /><span>before the first session.</span></h2>
            <p className="section-intro">Clear requirements and platform limits, up front.</p>
          </div>
          <Faq />
        </div>
      </section>

      <section className="closing-section">
        <div className="page-width closing-card">
          <div className="closing-mark" aria-hidden="true"><span /><span /><span /></div>
          <p className="eyebrow">The device is part of the workflow now</p>
          <h2>Connect your first<br /><span>mobile device.</span></h2>
          <div className="closing-actions">
            <a className={buttonVariants({ size: "lg", className: "button-lime" })} href={releases}>
              Download mobile-use <ArrowUpRight data-icon="inline-end" aria-hidden="true" />
            </a>
            <a className="closing-github" href={repo} target="_blank" rel="noreferrer">
              <CodeXml aria-hidden="true" /> View the source
            </a>
          </div>
          <p className="closing-note">Open source · Run locally · Bring your own devices</p>
        </div>
      </section>

      <footer className="site-footer">
        <div className="page-width site-footer__top">
          <a href="#top" className="brand brand--footer" aria-label="mobile-use, back to top">
            <span className="brand-mark" aria-hidden="true"><Smartphone /></span>
            <span>mobile-use</span>
          </a>
          <p>Mobile devices, connected to the tools you already use.</p>
          <nav aria-label="Footer links">
            <a href={guide}>Quickstart</a>
            <a href={releases}>Downloads</a>
            <a href={operations}>Deploy</a>
            <a href={verification}>Verification</a>
            <a href={repo} target="_blank" rel="noreferrer">GitHub <ArrowUpRight aria-hidden="true" /></a>
          </nav>
        </div>
        <Separator className="footer-divider" />
        <div className="page-width site-footer__bottom">
          <span>mobile-use</span>
          <span>Built for device owners and the agents they trust</span>
        </div>
      </footer>
    </main>
  )
}

function CapabilityVisual({ kind }: { kind: string }) {
  if (kind === "android") {
    return (
      <div className="cap-visual cap-visual--terminal" aria-hidden="true">
        <div className="terminal-top"><span /><span /><span /><span>Android connector</span></div>
        <div className="terminal-lines">
          <p><b>$</b> mobile-use devices</p>
          <p className="terminal-result"><i /> Pixel · authorized</p>
          <p><b>$</b> mobile-use mcp-config</p>
          <p className="terminal-muted">stdio · tools · permissions</p>
        </div>
      </div>
    )
  }
  if (kind === "companion") {
    return (
      <div className="cap-visual cap-visual--companion" aria-hidden="true">
        <div className="mini-phone"><div className="mini-phone__notch" /><div className="mini-phone__status"><i /> Sharing approved</div><div className="mini-phone__screen"><span /><span /><span /><span /></div><div className="mini-phone__home" /></div>
        <div className="capture-tag"><span className="capture-dot" /> Frame stream <b>Connected</b></div>
      </div>
    )
  }
  if (kind === "iphone") {
    return (
      <div className="cap-visual cap-visual--mac" aria-hidden="true">
        <div className="mac-window"><div className="mac-window__bar"><span /><span /><span /> device connector</div><div className="mac-window__body"><div className="mac-device"><Smartphone /></div><div><b>iPhone</b><small>Appium · WebDriverAgent</small><small className="mac-online"><i /> Ready for a session</small></div><ArrowRight /></div></div>
      </div>
    )
  }
  return (
    <div className="cap-visual cap-visual--remote" aria-hidden="true">
      <div className="remote-node remote-node--center"><span className="remote-core"><Radio /></span><b>mobile-use</b><small>HTTPS control plane</small></div>
      <div className="remote-node remote-node--left"><span className="remote-dot" /><b>Agent</b></div>
      <div className="remote-node remote-node--right"><span className="remote-dot" /><b>Connector</b></div>
      <div className="remote-path remote-path--left" /><div className="remote-path remote-path--right" />
    </div>
  )
}

function AccessStep({ number, title, description, status }: { number: string; title: string; description: string; status: string }) {
  return (
    <div className="access-step">
      <span className="access-step__number">{number}</span>
      <div className="access-step__content">
        <div className="access-step__title-row"><h3>{title}</h3><Badge variant="outline">{status}</Badge></div>
        <p>{description}</p>
      </div>
    </div>
  )
}

function StartStep({ number, title, children }: { number: string; title: string; children: React.ReactNode }) {
  return (
    <Card className="start-step">
      <CardHeader>
        <div className="start-step__index">{number}</div>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  )
}
