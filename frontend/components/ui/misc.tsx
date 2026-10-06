import * as React from "react";

import { cn } from "@/lib/utils";

const alertTones = {
  info: "border-info/25 bg-info-soft text-info",
  verified: "border-verified/25 bg-verified-soft text-verified",
  held: "border-held/25 bg-held-soft text-held",
  critical: "border-critical/25 bg-critical-soft text-critical",
  neutral: "border-line bg-subtle text-ink-soft",
} as const;

export function Alert({
  tone = "neutral",
  title,
  children,
  className,
}: {
  tone?: keyof typeof alertTones;
  title?: string;
  children?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("rounded-[var(--radius-card)] border px-4 py-3 text-xs", alertTones[tone], className)}>
      {title ? <div className="mb-0.5 text-sm font-medium">{title}</div> : null}
      {children ? <div className="leading-relaxed">{children}</div> : null}
    </div>
  );
}

export function Skeleton({ className }: { className?: string }) {
  return <div className={cn("animate-pulse rounded-md bg-line", className)} />;
}

export function EmptyState({
  title,
  description,
  action,
  className,
}: {
  title: string;
  description?: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center gap-2 rounded-[var(--radius-card)] border border-dashed border-line-strong bg-subtle px-6 py-10 text-center",
        className,
      )}
    >
      <p className="text-sm font-medium text-ink">{title}</p>
      {description ? <p className="max-w-md text-xs text-ink-faint">{description}</p> : null}
      {action ? <div className="pt-2">{action}</div> : null}
    </div>
  );
}

export function SectionHeading({
  title,
  description,
  action,
  className,
}: {
  title: string;
  description?: React.ReactNode;
  action?: React.ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex flex-wrap items-end justify-between gap-3", className)}>
      <div className="flex flex-col gap-0.5">
        <h2 className="text-base font-semibold tracking-tight text-ink">{title}</h2>
        {description ? <p className="max-w-2xl text-xs text-ink-faint">{description}</p> : null}
      </div>
      {action}
    </div>
  );
}

export function PageHeader({
  eyebrow,
  title,
  description,
  action,
}: {
  eyebrow?: string;
  title: string;
  description?: React.ReactNode;
  action?: React.ReactNode;
}) {
  return (
    <header className="flex flex-wrap items-start justify-between gap-4">
      <div className="flex flex-col gap-1">
        {eyebrow ? (
          <span className="text-[11px] font-medium uppercase tracking-[0.08em] text-ink-faint">{eyebrow}</span>
        ) : null}
        <h1 className="text-2xl font-semibold tracking-tight text-ink">{title}</h1>
        {description ? <p className="max-w-3xl text-sm text-ink-soft">{description}</p> : null}
      </div>
      {action}
    </header>
  );
}

/** Definition list used for policy checks, FX notes and evidence metadata. */
export function KeyValues({ items }: { items: { label: string; value: React.ReactNode }[] }) {
  return (
    <dl className="divide-y divide-line">
      {items.map((item) => (
        <div key={item.label} className="flex items-start justify-between gap-4 py-1.5">
          <dt className="text-xs text-ink-faint">{item.label}</dt>
          <dd className="tnum text-right text-xs font-medium text-ink">{item.value}</dd>
        </div>
      ))}
    </dl>
  );
}
