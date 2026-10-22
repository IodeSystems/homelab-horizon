/**
 * The type-in filter on `/services`. Like the checks status filter, it lives in
 * the URL (`?q=`), never in component state, so a filtered view has its own
 * address.
 *
 * Every whitespace-separated term must match (AND), case-insensitively, as a
 * substring of one of: the name, the project, the rung, a domain, or what the
 * service serves (its backend or static root). "redline staging" narrows to
 * redline's staging services; it does not widen to either.
 */
import type { Service } from "../api/types";

/** `?q=` from a raw search value: absent / blank / non-string means no filter.
 * The text is kept AS TYPED, never trimmed: the input is bound to it, and
 * trimming would delete the space typed before a second term. */
export function parseQueryParam(raw: unknown): string | undefined {
  if (typeof raw !== "string") return undefined;
  return raw.trim() === "" ? undefined : raw;
}

type Filterable = Pick<Service, "name" | "project" | "environment" | "domains"> & {
  proxy?: { backend: string; staticRoot?: string };
};

function haystack(s: Filterable): string[] {
  return [
    s.name,
    s.project ?? "",
    s.environment ?? "",
    ...s.domains,
    s.proxy?.backend ?? "",
    s.proxy?.staticRoot ?? "",
  ].map((v) => v.toLowerCase());
}

export function matchesServiceQuery(s: Filterable, q: string | undefined): boolean {
  if (q === undefined) return true;
  const fields = haystack(s);
  return q
    .toLowerCase()
    .split(/\s+/)
    .filter((t) => t !== "")
    .every((term) => fields.some((f) => f.includes(term)));
}
