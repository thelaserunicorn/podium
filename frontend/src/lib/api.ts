// api.ts — single fetch client. credentials: "include" is mandatory so the
// session cookie travels on every request (AGENTS.md §6).

export interface ApiError extends Error {
  status: number;
  code?: string;
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
  initOverride?: RequestInit,
): Promise<T> {
  const init: RequestInit = {
    method,
    credentials: "include",
    headers: body ? { "Content-Type": "application/json" } : {},
    ...initOverride,
  };
  if (body !== undefined) {
    init.body = JSON.stringify(body);
  }
  const res = await fetch(path, init);
  const text = await res.text();
  if (!res.ok) {
    let code: string | undefined;
    try {
      const parsed = JSON.parse(text) as { error?: string; message?: string };
      code = parsed.error;
      throw Object.assign(new Error(parsed.message ?? res.statusText), {
        status: res.status,
        code,
      }) as ApiError;
    } catch (e) {
      if (e instanceof Error && "status" in e) throw e;
      throw Object.assign(new Error(text || res.statusText), {
        status: res.status,
      }) as ApiError;
    }
  }
  if (!text) return undefined as T;
  return JSON.parse(text) as T;
}

export const api = {
  get: <T>(path: string, init?: RequestInit) => request<T>("GET", path, undefined, init),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body),
  put: <T>(path: string, body?: unknown) => request<T>("PUT", path, body),
  del: <T>(path: string) => request<T>("DELETE", path),
};
