"use client"

import { useState } from "react"
import { ArrowUpRight, Menu, Smartphone } from "lucide-react"

import { buttonVariants } from "@/components/ui/button"
import { Sheet, SheetContent, SheetDescription, SheetFooter, SheetHeader, SheetTitle, SheetTrigger } from "@/components/ui/sheet"
import { cn } from "@/lib/utils"

const links = [
  { label: "Workflow", href: "#how-it-works" },
  { label: "Capabilities", href: "#capabilities" },
  { label: "Quickstart", href: "#start" },
  { label: "Questions", href: "#faq" },
]

export function SiteNav() {
  const [open, setOpen] = useState(false)

  return (
    <header id="top" className="site-nav">
      <div className="page-width site-nav__inner">
        <a href="#top" className="brand" aria-label="mobile-use home">
          <span className="brand-mark" aria-hidden="true"><Smartphone /></span>
          <span>mobile-use</span>
        </a>
        <nav className="site-nav__links" aria-label="Main navigation">
          {links.map((link) => <a key={link.href} href={link.href}>{link.label}</a>)}
        </nav>
        <div className="site-nav__actions">
          <a className={cn(buttonVariants({ size: "sm" }), "nav-download")} href="https://github.com/UpstandPlatform/mobile-use/releases/latest">
            Download <ArrowUpRight data-icon="inline-end" aria-hidden="true" />
          </a>
          <Sheet open={open} onOpenChange={setOpen}>
            <SheetTrigger className="mobile-menu-trigger" aria-label="Open navigation">
              <Menu aria-hidden="true" />
            </SheetTrigger>
            <SheetContent side="right" className="mobile-nav-sheet">
              <SheetHeader>
                <SheetTitle>Explore mobile-use</SheetTitle>
                <SheetDescription>Choose a section or get the app.</SheetDescription>
              </SheetHeader>
              <nav className="mobile-nav-links" aria-label="Mobile navigation">
                {links.map((link, index) => (
                  <a key={link.href} href={link.href} onClick={() => setOpen(false)}>
                    <span>0{index + 1}</span>{link.label}<ArrowUpRight aria-hidden="true" />
                  </a>
                ))}
              </nav>
              <SheetFooter className="mobile-nav-footer">
                <a className={buttonVariants({ size: "lg", className: "button-lime" })} href="https://github.com/UpstandPlatform/mobile-use/releases/latest" onClick={() => setOpen(false)}>
                  Download mobile-use <ArrowUpRight data-icon="inline-end" aria-hidden="true" />
                </a>
              </SheetFooter>
            </SheetContent>
          </Sheet>
        </div>
      </div>
    </header>
  )
}
