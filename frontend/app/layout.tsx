import type { Metadata } from "next";

import { AppShell } from "@/components/app-shell";
import { SessionProvider } from "@/lib/session";

import "./globals.css";

export const metadata: Metadata = {
  title: {
    default: "ImpactPay — funding tied to what it accomplishes",
    template: "%s · ImpactPay",
  },
  description:
    "ImpactPay connects funding to measurable milestones. Evidence is verified against the milestone's requirements before any money moves, and every donor can trace their contribution to the outcome it funded.",
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen">
        <SessionProvider>
          <AppShell>{children}</AppShell>
        </SessionProvider>
      </body>
    </html>
  );
}
