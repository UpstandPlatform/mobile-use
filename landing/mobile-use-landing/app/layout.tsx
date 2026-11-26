import type { Metadata } from "next"
import { Geist, Geist_Mono } from "next/font/google"

import "./globals.css"

const geist = Geist({ subsets: ["latin"], variable: "--font-geist-sans" })
const geistMono = Geist_Mono({ subsets: ["latin"], variable: "--font-geist-mono" })

export const metadata: Metadata = {
  title: "mobile-use — bring mobile devices into the agent loop",
  description:
    "Connect Android and iPhone devices to a local-first operator console and MCP agents. Observe screens, approve sessions, and act through explicit device capabilities.",
  applicationName: "mobile-use",
  openGraph: {
    title: "mobile-use — bring mobile devices into the agent loop",
    description:
      "A local-first control plane for real mobile devices, operator approvals, and MCP agents.",
    siteName: "mobile-use",
    type: "website",
  },
  twitter: {
    card: "summary_large_image",
    title: "mobile-use — bring mobile devices into the agent loop",
    description:
      "A local-first control plane for real mobile devices, operator approvals, and MCP agents.",
  },
}

export default function RootLayout({ children }: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en" className={`dark ${geist.variable} ${geistMono.variable}`}>
      <body>{children}</body>
    </html>
  )
}
