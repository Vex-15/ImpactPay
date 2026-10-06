import * as React from "react";

import { humanize } from "@/lib/format";
import type { Decision, MilestoneStatus } from "@/lib/types";
import { cn } from "@/lib/utils";

type Tone = "neutral" | "accent" | "verified" | "held" | "critical" | "info" | "simulated";

const tones: Record<Tone, string> = {
  neutral: "border-line-strong bg-subtle text-ink-soft",
  accent: "border-accent/30 bg-accent-soft text-accent-strong",
  verified: "border-verified/25 bg-verified-soft text-verified",
  held: "border-held/25 bg-held-soft text-held",
  critical: "border-critical/25 bg-critical-soft text-critical",
  info: "border-info/25 bg-info-soft text-info",
  simulated: "border-simulated/25 bg-simulated-soft text-simulated",
};

export function Badge({
  tone = "neutral",
  className,
  children,
  ...props
}: React.HTMLAttributes<HTMLSpanElement> & { tone?: Tone }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium leading-5",
        tones[tone],
        className,
      )}
      {...props}
    >
      {children}
    </span>
  );
}

const milestoneTones: Record<MilestoneStatus, Tone> = {
  PENDING: "neutral",
  IN_PROGRESS: "info",
  EVIDENCE_SUBMITTED: "info",
  VERIFYING: "info",
  APPROVED: "verified",
  HELD: "held",
  MORE_EVIDENCE_REQUESTED: "held",
  PAYMENT_RELEASED: "accent",
  COMPLETED: "verified",
  REFUNDED: "critical",
};

/** Milestone lifecycle badge. The colour means one thing only: verification state. */
export function MilestoneBadge({ status }: { status: MilestoneStatus }) {
  return <Badge tone={milestoneTones[status] ?? "neutral"}>{humanize(status)}</Badge>;
}

const decisionTones: Record<Decision, Tone> = {
  APPROVE: "verified",
  HOLD: "held",
  REQUEST_MORE_EVIDENCE: "info",
};

export function DecisionBadge({ decision }: { decision: Decision }) {
  const label =
    decision === "REQUEST_MORE_EVIDENCE" ? "More evidence requested" : humanize(decision);
  return <Badge tone={decisionTones[decision] ?? "neutral"}>{label}</Badge>;
}

/** The "verified evidence" mark shown next to a checked figure. */
export function VerifiedMark({ className }: { className?: string }) {
  return (
    <span
      title="Backed by evidence a verifier confirmed"
      className={cn("inline-flex items-center gap-1 text-[11px] font-medium text-verified", className)}
    >
      <svg viewBox="0 0 16 16" aria-hidden className="h-3.5 w-3.5" fill="none">
        <path
          d="M3 8.5 6.2 12 13 4.5"
          stroke="currentColor"
          strokeWidth="1.8"
          strokeLinecap="round"
          strokeLinejoin="round"
        />
      </svg>
      Verified
    </span>
  );
}

export function OrgVerifiedBadge({ verified }: { verified: boolean }) {
  return verified ? (
    <Badge tone="verified">Organization verified</Badge>
  ) : (
    <Badge tone="held">Awaiting platform review</Badge>
  );
}

export function SimulatedBadge({ simulated }: { simulated: boolean }) {
  if (!simulated) return <Badge tone="info">PayPal sandbox</Badge>;
  return (
    <Badge tone="simulated" title="No live PayPal credentials were configured, so this ran the deterministic simulation.">
      Sandbox simulation
    </Badge>
  );
}
