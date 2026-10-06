import { formatMoney, formatMoneyShort } from "@/lib/format";
import { cn } from "@/lib/utils";

/**
 * Money renders an integer minor-unit amount.
 *
 * It never converts between currencies and never rounds: the figure shown is the
 * figure stored. Amounts the API already labelled are passed through verbatim.
 */
export function Money({
  minor,
  currency = "INR",
  short = false,
  className,
  label,
}: {
  minor: number;
  currency?: string;
  short?: boolean;
  className?: string;
  label?: string;
}) {
  if (label) {
    return <span className={cn("tnum", className)}>{label}</span>;
  }
  return (
    <span className={cn("tnum", className)}>
      {short ? formatMoneyShort(minor, currency) : formatMoney(minor, currency)}
    </span>
  );
}
