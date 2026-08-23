/**
 * Correlation identifiers shared with the API's acceptance grammar
 * (ADR-0017): 1–128 characters of [A-Za-z0-9._~:@-]. The BFF accepts a
 * well-formed browser-provided id verbatim and generates one otherwise,
 * exactly like the API middleware does for its callers.
 */
const GRAMMAR = /^[A-Za-z0-9._~:@-]{1,128}$/;
export const REQUEST_ID_HEADER = "X-Request-ID";

export function validRequestId(value: string): boolean {
  return GRAMMAR.test(value);
}

/**
 * Resolves the effective correlation id for an inbound request: a
 * grammar-valid inbound header wins; anything else becomes a fresh
 * `req_…` identifier. Mirrors the API edge so an id can flow unchanged
 * from the browser's own client code through the BFF into /v1 events.
 */
export function resolveRequestId(inbound: string | null | undefined): {
  id: string;
  source: "header" | "generated";
} {
  if (inbound && validRequestId(inbound)) {
    return { id: inbound, source: "header" };
  }
  return { id: generateRequestId(), source: "generated" };
}

/** crypto.randomUUID() satisfies the grammar (36 chars of hex and dashes). */
export function generateRequestId(): string {
  return `web_${crypto.randomUUID()}`;
}
