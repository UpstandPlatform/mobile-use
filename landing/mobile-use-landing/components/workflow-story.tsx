"use client"

import { useRef, useState } from "react"
import { AnimatePresence, motion, useMotionValueEvent, useScroll, useTransform } from "motion/react"
import { ArrowDownRight, ArrowUpRight, Check, ChevronRight, CircleDot, Eye, Fingerprint, ScanSearch, Smartphone, Wifi } from "lucide-react"

import { cn } from "@/lib/utils"

const steps = [
  {
    index: "01",
    eyebrow: "Connect",
    title: "Bring the device into reach.",
    body: "Start with USB or Wireless Debugging. For a companion-only connection, pair over Wi-Fi with a QR code and keep screen capture under the phone owner’s control.",
    signal: "USB · Wi-Fi · QR pairing",
  },
  {
    index: "02",
    eyebrow: "Observe",
    title: "See the same screen the owner sees.",
    body: "The agent requests a session. The owner approves it, then the console receives the live screen. With an ADB or Appium connector, the agent can also inspect the current accessibility context.",
    signal: "Fresh frame · approved session",
  },
  {
    index: "03",
    eyebrow: "Act",
    title: "Turn context into a deliberate action.",
    body: "MCP tools find visible UI elements, inspect installed apps, and send a tap, gesture, key, or app command through the connector’s granted capabilities.",
    signal: "Find · verify · act",
  },
]

export function WorkflowStory() {
  const storyRef = useRef<HTMLDivElement>(null)
  const [active, setActive] = useState(0)
  const { scrollYProgress } = useScroll({ target: storyRef, offset: ["start start", "end end"] })
  const lineScale = useTransform(scrollYProgress, [0, 1], [0.08, 0.92])
  const screenY = useTransform(scrollYProgress, [0, 1], [10, -10])
  const screenRotate = useTransform(scrollYProgress, [0, 0.5, 1], [1.25, 0, -1.25])

  useMotionValueEvent(scrollYProgress, "change", (progress) => {
    setActive(Math.min(steps.length - 1, Math.floor(progress * steps.length)))
  })

  return (
    <section id="how-it-works" className="workflow-section section-pad">
      <div className="page-width">
        <div className="section-heading section-heading--split workflow-heading">
          <div>
            <p className="eyebrow">From device to action</p>
            <h2>One connected<br /><span>sequence.</span></h2>
          </div>
          <p className="section-intro">A useful agent session is not a magic jump. It is a connection, an observation, and an action with clear ownership between each step.</p>
        </div>

        <div ref={storyRef} className="workflow-track">
          <div className="workflow-sticky">
            <motion.div className="workflow-console" style={{ y: screenY, rotate: screenRotate }}>
              <div className="workflow-console__bar">
                <div className="window-controls"><i /><i /><i /></div>
                <span className="workflow-console__title">mobile-use <b>/</b> {steps[active].eyebrow.toLowerCase()}</span>
                <span className="workflow-console__live"><i /> local</span>
              </div>
              <div className="workflow-console__content">
                <div className="workflow-console__rail"><span className="rail-logo"><Smartphone /></span><i className="rail-selected" /><i /><i /><span className="rail-owner">LO</span></div>
                <AnimatePresence mode="wait" initial={false}>
                  <motion.div
                    key={active}
                    className="workflow-screen"
                    initial={{ opacity: 0, y: 8, filter: "blur(5px)" }}
                    animate={{ opacity: 1, y: 0, filter: "blur(0px)" }}
                    exit={{ opacity: 0, y: -6, filter: "blur(4px)" }}
                    transition={{ duration: 0.24, ease: "easeOut" }}
                  >
                    <div className="workflow-screen__meta"><span>STEP {steps[active].index}</span><span>{steps[active].signal}</span></div>
                    {active === 0 ? <ConnectScreen /> : active === 1 ? <ObserveScreen /> : <ActScreen />}
                  </motion.div>
                </AnimatePresence>
              </div>
              <div className="workflow-console__progress"><motion.span style={{ scaleX: lineScale, transformOrigin: "left" }} /></div>
            </motion.div>

            <motion.svg className="workflow-link" viewBox="0 0 520 400" fill="none" aria-hidden="true">
              <path d="M40 292C182 292 220 92 454 92" stroke="currentColor" strokeOpacity=".22" strokeWidth="1.5" strokeDasharray="3 8" />
              <motion.path d="M40 292C182 292 220 92 454 92" stroke="currentColor" strokeWidth="1.5" strokeDasharray="3 8" style={{ pathLength: lineScale }} />
              <circle cx="40" cy="292" r="4" fill="currentColor" />
              <circle cx="454" cy="92" r="4" fill="currentColor" />
            </motion.svg>

            <div className="workflow-phone" aria-hidden="true">
              <div className="workflow-phone__camera" />
              <div className="workflow-phone__status"><span>9:41</span><span><Wifi /><i /></span></div>
              <div className="workflow-phone__brand"><span><Smartphone /></span><b>mobile-use</b><CircleDot /></div>
              <div className="workflow-phone__body">
                <AnimatePresence mode="wait" initial={false}>
                  <motion.div key={`phone-${active}`} initial={{ opacity: 0 }} animate={{ opacity: 1 }} exit={{ opacity: 0 }} transition={{ duration: 0.16 }}>
                    {active === 0 ? <PhoneConnect /> : active === 1 ? <PhoneApprove /> : <PhoneReady />}
                  </motion.div>
                </AnimatePresence>
              </div>
              <div className="workflow-phone__home" />
            </div>
          </div>

          <div className="workflow-steps">
            {steps.map((step, index) => (
              <article key={step.index} className={cn("workflow-step", active === index && "workflow-step--active")} aria-current={active === index ? "step" : undefined}>
                <p className="workflow-step__index"><span>{step.index}</span><i />{step.eyebrow}</p>
                <h3>{step.title}</h3>
                <p className="workflow-step__body">{step.body}</p>
                <div className="workflow-step__signal"><span><Check /></span>{step.signal}</div>
              </article>
            ))}
          </div>
        </div>
      </div>
    </section>
  )
}

function ConnectScreen() {
  return (
    <>
      <h3>Choose the path your device supports</h3>
      <p>Connections stay visible in your inventory.</p>
      <div className="connection-options">
        <div><span><Smartphone /></span><b>Android · ADB</b><small>USB or Wireless Debugging</small><ChevronRight /></div>
        <div><span><Fingerprint /></span><b>Android · companion</b><small>Scan a QR code to pair</small><ChevronRight /></div>
        <div><span className="connection-option__muted"><Wifi /></span><b>iPhone connector</b><small>macOS · Appium · WDA</small><ChevronRight /></div>
      </div>
      <div className="connection-note"><CircleDot /> Owner approval is enabled for this device</div>
    </>
  )
}

function ObserveScreen() {
  return (
    <>
      <h3>Session approved</h3>
      <p>Screen sharing started after owner approval.</p>
      <div className="observed-frame">
        <div className="observed-frame__bar"><span><Eye /> ANDROID COMPANION</span><span><i /> LIVE</span></div>
        <div className="observed-frame__screen"><div className="observed-clock">09:41</div><div className="observed-search" /><div className="observed-tiles"><i /><i /><i /><i /></div><div className="observed-nav"><i /><i /><i /></div></div>
      </div>
      <div className="frame-meta"><span>Frame received</span><b>Owner-approved session</b></div>
    </>
  )
}

function ActScreen() {
  return (
    <>
      <h3>Find a target before you tap</h3>
      <p>MCP action uses the latest observed frame.</p>
      <div className="tool-call">
        <div className="tool-call__header"><ScanSearch /><span>ui_find</span><small>mobile-use MCP</small></div>
        <div className="tool-call__code"><span>query</span><code>&quot;Continue&quot;</code></div>
        <div className="tool-call__result"><span><Check /></span><div><b>Button · Continue</b><small>Visible · enabled · frame verified</small></div><ArrowUpRight /></div>
      </div>
      <div className="action-path"><span>Find target</span><ArrowDownRight /><span>Verify frame</span><ArrowDownRight /><b>Tap</b></div>
    </>
  )
}

function PhoneConnect() {
  return (
    <div className="phone-flow phone-flow--connect">
      <span className="phone-flow__icon"><Fingerprint /></span>
      <small>PAIR THIS DEVICE</small>
      <b>Connect to your workstation</b>
      <div className="phone-qr" aria-hidden="true"><i /><i /><i /><i /><i /><i /><i /><i /><i /><i /><i /><i /><i /><i /><i /><i /></div>
      <span className="phone-flow__hint">Scan the QR code shown in the console</span>
    </div>
  )
}

function PhoneApprove() {
  return (
    <div className="phone-flow phone-flow--approve">
      <span className="phone-flow__icon"><Eye /></span>
      <small>SESSION REQUEST</small>
      <b>Review agent access</b>
      <span className="phone-permission"><Eye /> Screen capture <i>Included</i></span>
      <span className="phone-permission"><Smartphone /> Sensor request <i>Included</i></span>
      <span className="phone-approve-button">Approve session <ChevronRight /></span>
    </div>
  )
}

function PhoneReady() {
  return (
    <div className="phone-flow phone-flow--ready">
      <span className="phone-flow__icon"><Check /></span>
      <small>SESSION ACTIVE</small>
      <b>You’re in control</b>
      <span className="phone-ready-copy">You can pause or stop the session at any time.</span>
      <span className="phone-ready-pill"><i /> Agent connected</span>
      <span className="phone-stop-button">End session</span>
    </div>
  )
}
