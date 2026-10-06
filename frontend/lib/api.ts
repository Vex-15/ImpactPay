// The single place that talks to the ImpactPay API.
//
// Two entry points exist because the app renders both on the server (public
// discovery pages) and in the browser (everything behind a session):
//
//   serverGet  — server components reading public data
//   api        — browser calls, sending the session cookie
//
// Session state lives in an httpOnly cookie set by the API, so the browser only
// ever needs `credentials: "include"`; no token is stored in JavaScript.

export const API_URL =
  process.env.NEXT_PUBLIC_API_URL ?? "http://localhost:8080";

const SERVER_URL = process.env.API_URL ?? API_URL;

export class ApiError extends Error {
  status: number;
  code: string;
  detail?: unknown;

  constructor(status: number, code: string, message: string, detail?: unknown) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.detail = detail;
  }
}

interface ErrorEnvelope {
  error?: { code?: string; message?: string; detail?: unknown };
}

function parseError(status: number, body: unknown): ApiError {
  const envelope = body as ErrorEnvelope | undefined;
  const code = envelope?.error?.code ?? "request_failed";
  const message = envelope?.error?.message ?? `Request failed with status ${status}.`;
  return new ApiError(status, code, message, envelope?.error?.detail);
}

/** Browser-facing request that carries the session cookie. */
export async function api<T>(
  path: string,
  init: RequestInit & { json?: unknown } = {},
): Promise<T> {
  const { json, headers, ...rest } = init;
  const response = await fetch(`${API_URL}${path}`, {
    ...rest,
    credentials: "include",
    headers: {
      Accept: "application/json",
      ...(json !== undefined ? { "Content-Type": "application/json" } : {}),
      ...(headers ?? {}),
    },
    body: json !== undefined ? JSON.stringify(json) : rest.body,
    cache: "no-store",
  });

  if (response.status === 204) return undefined as T;

  const text = await response.text();
  const payload = text ? safeJSON(text) : undefined;

  if (!response.ok) throw parseError(response.status, payload);
  return payload as T;
}

/** Multipart upload (evidence). Never sets Content-Type so the boundary is correct. */
export async function apiUpload<T>(path: string, form: FormData): Promise<T> {
  const response = await fetch(`${API_URL}${path}`, {
    method: "POST",
    credentials: "include",
    body: form,
  });
  const text = await response.text();
  const payload = text ? safeJSON(text) : undefined;
  if (!response.ok) throw parseError(response.status, payload);
  return payload as T;
}

/** Server-side read of a public endpoint. */
export async function serverGet<T>(path: string): Promise<T> {
  const response = await fetch(`${SERVER_URL}${path}`, {
    headers: { Accept: "application/json" },
    cache: "no-store",
  });
  const text = await response.text();
  const payload = text ? safeJSON(text) : undefined;
  if (!response.ok) throw parseError(response.status, payload);
  return payload as T;
}

/** Server-side read that returns null instead of throwing (used by pages that 404). */
export async function serverGetOrNull<T>(path: string): Promise<T | null> {
  try {
    return await serverGet<T>(path);
  } catch (error) {
    if (error instanceof ApiError && error.status === 404) return null;
    throw error;
  }
}

function safeJSON(text: string): unknown {
  try {
    return JSON.parse(text);
  } catch {
    return undefined;
  }
}

/** Evidence files are served by the API and require the session cookie. */
export function evidenceURL(id: string): string {
  return `${API_URL}/api/evidence/${id}/file`;
}
