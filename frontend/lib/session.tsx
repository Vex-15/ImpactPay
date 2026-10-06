"use client";

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
} from "react";

import { api, ApiError } from "@/lib/api";
import type { Organization, User } from "@/lib/types";

interface MeResponse {
  user: User;
  organization: Organization | null;
  unread_notifications: number;
}

interface SessionValue {
  user: User | null;
  organization: Organization | null;
  unread: number;
  loading: boolean;
  refresh: () => Promise<void>;
  login: (email: string, password: string) => Promise<User>;
  register: (input: {
    email: string;
    password: string;
    full_name: string;
    role?: string;
  }) => Promise<User>;
  logout: () => Promise<void>;
}

const SessionContext = createContext<SessionValue | null>(null);

export function SessionProvider({ children }: { children: React.ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [organization, setOrganization] = useState<Organization | null>(null);
  const [unread, setUnread] = useState(0);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    try {
      const me = await api<MeResponse>("/api/auth/me");
      setUser(me.user);
      setOrganization(me.organization);
      setUnread(me.unread_notifications ?? 0);
    } catch (error) {
      // A missing session is an ordinary state, not a failure.
      if (!(error instanceof ApiError) || error.status !== 401) {
        console.warn("session refresh failed", error);
      }
      setUser(null);
      setOrganization(null);
      setUnread(0);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const login = useCallback(
    async (email: string, password: string) => {
      const result = await api<{ user: User }>("/api/auth/login", {
        method: "POST",
        json: { email, password },
      });
      await refresh();
      return result.user;
    },
    [refresh],
  );

  const register = useCallback(
    async (input: {
      email: string;
      password: string;
      full_name: string;
      role?: string;
    }) => {
      const result = await api<{ user: User }>("/api/auth/register", {
        method: "POST",
        json: input,
      });
      await refresh();
      return result.user;
    },
    [refresh],
  );

  const logout = useCallback(async () => {
    try {
      await api("/api/auth/logout", { method: "POST" });
    } finally {
      setUser(null);
      setOrganization(null);
      setUnread(0);
    }
  }, []);

  const value = useMemo<SessionValue>(
    () => ({ user, organization, unread, loading, refresh, login, register, logout }),
    [user, organization, unread, loading, refresh, login, register, logout],
  );

  return <SessionContext.Provider value={value}>{children}</SessionContext.Provider>;
}

export function useSession(): SessionValue {
  const ctx = useContext(SessionContext);
  if (!ctx) throw new Error("useSession must be used inside <SessionProvider>");
  return ctx;
}
