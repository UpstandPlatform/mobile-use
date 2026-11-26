"use client"

import { useRef } from "react"
import { motion, useScroll, useTransform } from "motion/react"
import { ArrowDown, ArrowUpRight, Blocks, CircleCheck, CircleDot, Clock3, Smartphone, Wifi, Workflow } from "lucide-react"

import { Badge } from "@/components/ui/badge"
import { buttonVariants } from "@/components/ui/button"

export function HeroScene({ releases }: { releases: string }) {
  const heroRef = useRef<HTMLElement>(null)
  const { scrollYProgress } = useScroll({ target: heroRef, offset: ["start start", "end start"] })
  const sceneY = useTransform(scrollYProgress, [0, 1], [0, -44])
  const sceneOpacity = useTransform(scrollYProgress, [0, 0.9], [1, 0.72])

  return (
    <section ref={heroRef} className="hero-section">
      <div className="page-width hero-layout">
        <div className="hero-copy">
          <Badge variant="outline" className="hero-badge"><span className="badge-mark" /> Mobile control for MCP agents</Badge>
          <h1>Put a real device<br /><span>in the agent loop.</span></h1>
          <p className="hero-lede">Connect Android and iPhone devices to one operator console. Observe the screen, approve the session, and give your agent a clear path to act.</p>
          <div className="hero-actions">
            <a className={buttonVariants({ size: "lg", className: "button-lime" })} href={releases}>
              Get the desktop app <ArrowUpRight data-icon="inline-end" aria-hidden="true" />
            </a>
            <a className="hero-secondary" href="#how-it-works">See how it works <ArrowDown aria-hidden="true" /></a>
          </div>
          <div className="hero-facts">
            <span>Local first</span><i aria-hidden="true" />
            <span>USB or Wi-Fi</span><i aria-hidden="true" />
            <span>Owner approved</span>
          </div>
        </div>

        <motion.div className="hero-art" style={{ y: sceneY, opacity: sceneOpacity }} aria-label="Illustration of a connected device in the mobile-use console">
          <svg className="hero-link" viewBox="0 0 820 510" fill="none" aria-hidden="true">
            <motion.path d="M357 263C486 263 466 148 580 148" stroke="currentColor" strokeWidth="1.4" strokeDasharray="4 7" style={{ pathLength: useTransform(scrollYProgress, [0, 0.7], [0.84, 0.18]) }} />
            <circle cx="357" cy="263" r="4" fill="currentColor" />
            <circle cx="580" cy="148" r="4" fill="currentColor" />
          </svg>
          <ConsolePreview />
          <PhonePreview />
          <div className="hero-caption"><span className="capture-dot" /> One session, from screen to tool call</div>
        </motion.div>
      </div>
      <div className="hero-bottom page-width">
        <span>Open source device control</span>
        <span>Android companion · ADB · Appium connector</span>
      </div>
    </section>
  )
}

function ConsolePreview() {
  return (
    <div className="console-preview" aria-hidden="true">
      <div className="console-preview__chrome">
        <div className="window-controls"><i /><i /><i /></div>
        <span className="preview-address"><span /> localhost:8787</span>
        <span className="preview-local">local</span>
      </div>
      <div className="console-preview__body">
        <aside className="preview-sidebar">
          <div className="preview-brand"><span className="preview-brand__mark"><Smartphone /></span><b>mobile-use</b></div>
          <div className="preview-nav preview-nav--active"><Blocks className="preview-nav__glyph" /> Devices <i>1</i></div>
          <div className="preview-nav"><Clock3 className="preview-nav__glyph" /> Sessions</div>
          <div className="preview-nav"><Workflow className="preview-nav__glyph" /> MCP & Agents</div>
          <div className="preview-sidebar__bottom"><span className="preview-avatar">LO</span><div><b>Local owner</b><small>Administrator</small></div></div>
        </aside>
        <div className="preview-main">
          <div className="preview-main__heading"><div><small>DEVICE INVENTORY</small><h3>Connected devices</h3></div><span className="preview-refresh">Refresh</span></div>
          <div className="preview-device-row">
            <span className="device-glyph"><Smartphone /></span>
            <div className="device-name"><b>Android companion</b><small>Paired · owner approval</small></div>
            <span className="device-status"><i /> Online</span>
          </div>
          <div className="preview-live">
            <div className="preview-live__bar"><span>LIVE SCREEN</span><span><i /> Sharing</span></div>
            <div className="preview-live__screen">
              <div className="screen-time">9:41</div>
              <div className="screen-apps"><span /><span /><span /><span /></div>
              <div className="screen-dock"><i /><i /><i /><i /></div>
            </div>
          </div>
          <div className="preview-session"><span><CircleCheck /> Personal session</span><b>Owner approved</b><small>Screen · Apps · Touch</small></div>
        </div>
      </div>
    </div>
  )
}

function PhonePreview() {
  return (
    <div className="phone-preview" aria-hidden="true">
      <div className="phone-preview__frame">
        <div className="phone-preview__camera" />
        <div className="phone-preview__status"><span>9:41</span><span><Wifi /><i /><b>82</b></span></div>
        <div className="phone-preview__appbar"><span className="phone-app-mark"><Smartphone /></span><div><b>mobile-use</b><small>Device connected</small></div><CircleDot /></div>
        <div className="phone-preview__card"><small>SESSION REQUEST</small><b>Agent session</b><span>Review requested access</span><div className="phone-grants"><i /> Screen share <i /> Sensors</div><div className="phone-approve">Review request <ArrowUpRight /></div></div>
        <div className="phone-preview__footer"><span className="phone-lock" /> Your device, your approval</div>
        <div className="phone-preview__home" />
      </div>
    </div>
  )
}
