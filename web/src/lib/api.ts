import { apiBase, csrfHeaders, recoverFromUnauthorized } from "./auth";

function isMutating(method: string): boolean {
  return method !== "GET" && method !== "HEAD" && method !== "OPTIONS";
}

let writes = 0;

export function apiWriteCount(): number {
  return writes;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
    public body?: unknown,
  ) {
    super(message);
    this.name = "ApiError";
  }
}

interface RequestOpts {
  contentType?: string;
  parse?: "json" | "text";
  control?: boolean;
}

async function request<T = unknown>(
  method: string,
  path: string,
  body?: unknown,
  retry = true,
  opts: RequestOpts = {},
): Promise<T> {
  const headers: Record<string, string> = {
    ...(body !== undefined ? { "Content-Type": opts.contentType ?? "application/json" } : {}),
    ...(isMutating(method) ? csrfHeaders() : {}),
  };

  const resp = await fetch(`${apiBase()}${path}`, {
    method,
    headers,
    credentials: "include",
    body:
      body === undefined ? undefined : opts.contentType ? (body as string) : JSON.stringify(body),
  });

  if (resp.status === 401 && retry && (await recoverFromUnauthorized())) {
    return request<T>(method, path, body, false, opts);
  }

  if (!resp.ok) {
    let msg = `HTTP ${resp.status}`;
    let errBody: unknown;
    try {
      const j = (await resp.json()) as { error?: string; message?: string };
      errBody = j;
      msg = j.error ?? j.message ?? msg;
    } catch {
      // ignore — body may not be JSON
    }
    throw new ApiError(resp.status, msg, errBody);
  }
  if (isMutating(method) && !opts.control) writes++;

  if (resp.status === 204) return undefined as T;

  if (opts.parse === "text") return (await resp.text()) as T;
  return resp.json() as Promise<T>;
}

export const api = {
  get<T = unknown>(path: string): Promise<T> {
    return request<T>("GET", path);
  },
  post<T = unknown>(path: string, body?: unknown): Promise<T> {
    return request<T>("POST", path, body);
  },
  control<T = unknown>(path: string, body?: unknown): Promise<T> {
    return request<T>("POST", path, body, true, { control: true });
  },
  patch<T = unknown>(path: string, body?: unknown): Promise<T> {
    return request<T>("PATCH", path, body);
  },
  put<T = unknown>(path: string, body?: unknown): Promise<T> {
    return request<T>("PUT", path, body);
  },
  del<T = unknown>(path: string, body?: unknown): Promise<T> {
    return request<T>("DELETE", path, body);
  },
  getText(path: string): Promise<string> {
    return request<string>("GET", path, undefined, true, { parse: "text" });
  },
  putText(path: string, body: string): Promise<unknown> {
    return request<unknown>("PUT", path, body, true, { contentType: "text/plain", parse: "text" });
  },
};
