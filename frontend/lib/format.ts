// Formatting helpers.
//
// Money arrives from the API as integer minor units plus a currency code. The
// API also sends a pre-rendered label, and this module reproduces the same rules
// for client-side arithmetic so both agree.

const SYMBOLS: Record<string, string> = {
  INR: "\u20b9",
  USD: "$",
  EUR: "\u20ac",
  GBP: "\u00a3",
};

export function currencySymbol(currency: string): string {
  return SYMBOLS[currency] ?? "";
}

function groupThousands(whole: string): string {
  // Indian grouping for INR (12,34,567), western grouping otherwise.
  if (whole.length <= 3) return whole;
  const last3 = whole.slice(-3);
  let rest = whole.slice(0, -3);
  const parts: string[] = [];
  while (rest.length > 2) {
    parts.unshift(rest.slice(-2));
    rest = rest.slice(0, -2);
  }
  if (rest.length > 0) parts.unshift(rest);
  return [...parts, last3].join(",");
}

/** Format minor units as a full amount, e.g. 1000000 INR -> "₹10,000.00". */
export function formatMoney(minor: number, currency = "INR"): string {
  const negative = minor < 0;
  const abs = Math.abs(Math.trunc(minor));
  const whole = groupThousands(String(Math.floor(abs / 100)));
  const frac = String(abs % 100).padStart(2, "0");
  return `${negative ? "-" : ""}${currencySymbol(currency)}${whole}.${frac}`;
}

/** Format minor units compactly, e.g. 72000000 INR -> "₹7.2L", 150000 USD -> "$1.5K". */
export function formatMoneyShort(minor: number, currency = "INR"): string {
  const negative = minor < 0;
  const value = Math.abs(minor) / 100;
  const symbol = currencySymbol(currency);
  const sign = negative ? "-" : "";

  if (currency === "INR") {
    if (value >= 1e7) return `${sign}${symbol}${trim(value / 1e7)}Cr`;
    if (value >= 1e5) return `${sign}${symbol}${trim(value / 1e5)}L`;
    if (value >= 1e3) return `${sign}${symbol}${trim(value / 1e3)}K`;
    return `${sign}${symbol}${trim(value)}`;
  }
  if (value >= 1e9) return `${sign}${symbol}${trim(value / 1e9)}B`;
  if (value >= 1e6) return `${sign}${symbol}${trim(value / 1e6)}M`;
  if (value >= 1e3) return `${sign}${symbol}${trim(value / 1e3)}K`;
  return `${sign}${symbol}${trim(value)}`;
}

function trim(v: number): string {
  const rounded = Math.round(v * 10) / 10;
  return Number.isInteger(rounded) ? String(rounded) : rounded.toFixed(1);
}

/** Prefer the label the API computed so client and server never disagree. */
export function money(view: { label: string } | undefined, minor: number, currency: string): string {
  return view?.label ?? formatMoney(minor, currency);
}

export function percent(value: number, digits = 0): string {
  return `${(value * 100).toFixed(digits)}%`;
}

export function formatDate(iso?: string): string {
  if (!iso) return "\u2014";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "\u2014";
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}

export function formatDateTime(iso?: string): string {
  if (!iso) return "\u2014";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return "\u2014";
  return d.toLocaleString("en-GB", {
    day: "numeric",
    month: "short",
    year: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
}

export function relativeTime(iso?: string): string {
  if (!iso) return "";
  const then = new Date(iso).getTime();
  if (Number.isNaN(then)) return "";
  const diff = Date.now() - then;
  const minutes = Math.round(diff / 60000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.round(hours / 24);
  if (days < 30) return `${days}d ago`;
  return formatDate(iso);
}

export function formatBytes(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(0)} KB`;
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
}

/** Turn SCREAMING_SNAKE identifiers into readable labels. */
export function humanize(value?: string): string {
  if (!value) return "\u2014";
  return value
    .toLowerCase()
    .split(/[_\s]+/)
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

export function unitLabel(count: number, label: string): string {
  return `${count.toLocaleString("en-IN")} ${count === 1 ? label : plural(label)}`;
}

export function plural(label: string): string {
  if (/s$/i.test(label)) return label;
  if (/y$/i.test(label)) return `${label.slice(0, -1)}ies`;
  return `${label}s`;
}
