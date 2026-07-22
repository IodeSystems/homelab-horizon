import type { ZodType } from "zod/v4";

const API_BASE = "/api/v1";

export class ApiError extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

/**
 * Fetch from the API with optional zod schema validation.
 *
 * When a schema is provided, the response is validated against it.
 * In development, validation errors are logged as warnings.
 * This catches API drift (Go changed a field name/type) immediately
 * instead of silently producing undefined values that crash later.
 */
export async function apiFetch<T>(
  path: string,
  options?: RequestInit & { schema?: ZodType<T> },
): Promise<T> {
  const { schema, ...fetchOptions } = options ?? {};

  const res = await fetch(`${API_BASE}${path}`, {
    ...fetchOptions,
    headers: {
      "Content-Type": "application/json",
      ...fetchOptions?.headers,
    },
  });

  if (res.status === 401) {
    throw new ApiError(401, "Unauthorized");
  }

  if (!res.ok) {
    const body = await res.json().catch(() => ({ error: res.statusText }));
    throw new ApiError(res.status, body.error || res.statusText);
  }

  const data = await res.json();

  if (schema) {
    const result = schema.safeParse(data);
    if (!result.success) {
      console.warn(
        `[API] Response validation failed for ${path}:`,
        result.error.issues,
      );
    }
  }

  return data as T;
}

/**
 * Fetch a plain-text/YAML/shell-script response from a path OUTSIDE the
 * /api/v1 prefix (e.g. /integration/prometheus/scrape.yaml). Used for the
 * generated Prometheus config + install script, which are served as raw
 * text rather than JSON.
 */
export async function apiFetchText(path: string): Promise<string> {
  const res = await fetch(path, { credentials: "include" });

  if (res.status === 401) {
    throw new ApiError(401, "Unauthorized");
  }
  if (!res.ok) {
    throw new ApiError(res.status, res.statusText);
  }

  return res.text();
}
