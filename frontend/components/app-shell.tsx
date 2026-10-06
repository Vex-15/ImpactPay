"use client";

import Link from "next/link";
import { usePathname, useRouter } from "next/navigation";
import * as React from "react";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Dialog, DialogContent, DialogTrigger } from "@/components/ui/dialog";
import { api } from "@/lib/api";
import { relativeTime } from "@/lib/format";
import { useSession } from "@/lib/session";
import type { Notification } from "@/lib/types";
import { cn } from "@/lib/utils";

interface Health {
  env: string;
  demo: boolean;
  payments: { provider: string; live: boolean; simulated: boolean; account_currency: string };
  ai: { mode: string; provider: string };
}

const publicLinks = [
  { href: "/projects", label: "Projects" },
  { href: "/organizations", label: "Organizations" },
];

export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const { user, organization, unread, loading, logout } = useSession();
  const [health, setHealth] = React.useState<Health | null>(null);

  React.useEffect(() => {
    let cancelled = false;
    api<Health>("/api/health")
      .then((h) => {
        if (!cancelled) setHealth(h);
      })
      .catch(() => undefined);
    return () => {
      cancelled = true;
    };
  }, []);

  const dashboardHref = React.useMemo(() => {
    if (!user) return "/login";
    if (user.role === "admin") return "/admin/verification";
    if (organization) return "/dashboard/organization";
    return "/dashboard/donor";
  }, [user, organization]);

  return (
    <div className="flex min-h-screen flex-col">
      <header className="sticky top-0 z-30 border-b border-line bg-surface/95 backdrop-blur">
        <div className="mx-auto flex h-14 w-full max-w-[1200px] items-center gap-4 px-4">
          <Link href="/" className="flex items-center gap-2">
            <span aria-hidden className="grid h-6 w-6 place-items-center rounded bg-accent text-[11px] font-bold text-white">
              IP
            </span>
            <span className="text-sm font-semibold tracking-tight text-ink">ImpactPay</span>
          </Link>

          <nav className="hidden items-center gap-1 md:flex">
            {publicLinks.map((link) => (
              <NavLink key={link.href} href={link.href} active={pathname.startsWith(link.href)}>
                {link.label}
              </NavLink>
            ))}
            {user ? (
              <NavLink href={dashboardHref} active={pathname.startsWith("/dashboard") || pathname.startsWith("/admin")}>
                Dashboard
              </NavLink>
            ) : null}
            {user?.role === "admin" ? (
              <NavLink href="/admin/audit" active={pathname.startsWith("/admin/audit")}>
                Audit
              </NavLink>
            ) : null}
          </nav>

          <div className="ml-auto flex items-center gap-2">
            {health ? (
              <span
                className="hidden lg:inline-flex"
                title={`Payment rail: ${health.payments.provider}. Account currency ${health.payments.account_currency}. Narratives: ${health.ai.mode}.`}
              >
                <Badge tone={health.payments.simulated ? "simulated" : "info"}>
                  {health.payments.simulated ? "PayPal sandbox · simulated rail" : "PayPal sandbox"}
                </Badge>
              </span>
            ) : null}

            {user ? <NotificationsButton count={unread} /> : null}

            {loading ? null : user ? (
              <div className="flex items-center gap-2">
                <div className="hidden text-right sm:block">
                  <div className="text-xs font-medium leading-tight text-ink">{user.full_name}</div>
                  <div className="text-[11px] capitalize leading-tight text-ink-faint">
                    {organization ? "organization" : user.role}
                  </div>
                </div>
                <Button
                  variant="secondary"
                  size="sm"
                  onClick={async () => {
                    await logout();
                    router.push("/");
                  }}
                >
                  Sign out
                </Button>
              </div>
            ) : (
              <div className="flex items-center gap-2">
                <Link href="/login" className="text-xs font-medium text-ink-soft hover:text-ink">
                  Sign in
                </Link>
                <Button size="sm" onClick={() => router.push("/register")}>
                  Create account
                </Button>
              </div>
            )}
          </div>
        </div>
      </header>

      <main className="mx-auto w-full max-w-[1200px] flex-1 px-4 py-6">{children}</main>

      <footer className="border-t border-line bg-surface">
        <div className="mx-auto flex w-full max-w-[1200px] flex-col gap-1 px-4 py-5 text-[11px] text-ink-faint">
          <p className="text-ink-soft">
            ImpactPay doesn&apos;t just show where money went. It shows what the money accomplished.
          </p>
          <p>
            Prototype. All PayPal traffic is sandbox-only. Funding is held in ImpactPay&apos;s own ledger and
            released milestone by milestone; this is not a regulated escrow service.
          </p>
        </div>
      </footer>
    </div>
  );
}

function NavLink({ href, active, children }: { href: string; active: boolean; children: React.ReactNode }) {
  return (
    <Link
      href={href}
      className={cn(
        "rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors",
        active ? "bg-accent-soft text-accent-strong" : "text-ink-soft hover:bg-subtle hover:text-ink",
      )}
    >
      {children}
    </Link>
  );
}

function NotificationsButton({ count }: { count: number }) {
  const [items, setItems] = React.useState<Notification[]>([]);
  const [open, setOpen] = React.useState(false);

  const load = React.useCallback(async () => {
    try {
      const res = await api<{ notifications: Notification[] }>("/api/notifications?limit=20");
      setItems(res.notifications ?? []);
    } catch {
      setItems([]);
    }
  }, []);

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        setOpen(next);
        if (next) void load();
      }}
    >
      <DialogTrigger asChild>
        <Button variant="secondary" size="sm" aria-label="Notifications">
          Notifications
          {count > 0 ? (
            <span className="ml-1 rounded-full bg-accent px-1.5 text-[10px] font-semibold text-white">{count}</span>
          ) : null}
        </Button>
      </DialogTrigger>
      <DialogContent title="Notifications" description="Everything ImpactPay told you about your funding and evidence.">
        {items.length === 0 ? (
          <p className="text-xs text-ink-faint">Nothing yet.</p>
        ) : (
          <ul className="flex flex-col divide-y divide-line">
            {items.map((n) => (
              <li key={n.id} className="flex flex-col gap-0.5 py-2.5">
                <div className="flex items-baseline justify-between gap-3">
                  <span className="text-xs font-medium text-ink">{n.title}</span>
                  <span className="text-[11px] text-ink-faint">{relativeTime(n.created_at)}</span>
                </div>
                <p className="text-xs text-ink-soft">{n.body}</p>
                {n.link ? (
                  <Link href={n.link} className="text-[11px] font-medium text-accent hover:underline">
                    Open
                  </Link>
                ) : null}
              </li>
            ))}
          </ul>
        )}
        <div className="mt-3">
          <Button
            variant="secondary"
            size="sm"
            onClick={async () => {
              await api("/api/notifications/read", { method: "POST", json: {} });
              await load();
            }}
          >
            Mark all as read
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}
