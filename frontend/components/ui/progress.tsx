import { cn } from "@/lib/utils";

export function Progress({
  value,
  tone = "accent",
  className,
  label,
}: {
  /** Fraction in the range 0..1. */
  value: number;
  tone?: "accent" | "verified" | "held";
  className?: string;
  label?: string;
}) {
  const pct = Math.max(0, Math.min(1, value));
  const tones = {
    accent: "bg-accent",
    verified: "bg-verified",
    held: "bg-held",
  } as const;
  return (
    <div
      role="progressbar"
      aria-valuenow={Math.round(pct * 100)}
      aria-valuemin={0}
      aria-valuemax={100}
      aria-label={label}
      className={cn("h-1.5 w-full overflow-hidden rounded-full bg-line", className)}
    >
      <div className={cn("h-full rounded-full transition-[width]", tones[tone])} style={{ width: `${pct * 100}%` }} />
    </div>
  );
}

/**
 * FundingBar distinguishes captured, released and still-committed money so a
 * donor can see how much of a project's funding has actually moved.
 */
export function FundingBar({
  funded,
  goal,
  released,
  className,
}: {
  funded: number;
  goal: number;
  released: number;
  className?: string;
}) {
  const fundedPct = goal > 0 ? Math.min(100, (funded / goal) * 100) : 0;
  const releasedPct = goal > 0 ? Math.min(fundedPct, (released / goal) * 100) : 0;
  return (
    <div className={cn("relative h-2 w-full overflow-hidden rounded-full bg-line", className)}>
      <div className="absolute inset-y-0 left-0 rounded-full bg-accent/25" style={{ width: `${fundedPct}%` }} />
      <div className="absolute inset-y-0 left-0 rounded-full bg-verified" style={{ width: `${releasedPct}%` }} />
    </div>
  );
}
