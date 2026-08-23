/**
 * Redirect and origin safety helpers. Post-login redirects never leave this
 * origin: only relative paths inside the app are honored, so an attacker
 * cannot turn the login flow into an open redirector.
 */

const MAX_REDIRECT_LENGTH = 512;

/** Returns a safe in-app path, or undefined when the input is not one. */
export function safeRedirectPath(value: string | null | undefined): string | undefined {
  if (!value || value.length > MAX_REDIRECT_LENGTH) return undefined;
  if (!value.startsWith("/")) return undefined;
  // Protocol-relative and backslash tricks resolve off-origin in browsers.
  if (value.startsWith("//") || value.startsWith("/\\")) return undefined;
  if (/[\u0000-\u001f\u007f]/.test(value)) return undefined;
  if (value.includes("\\")) return undefined;
  return value;
}

/**
 * Same-origin enforcement for mutating BFF routes (the CSRF posture on top
 * of SameSite=Lax cookies): a browser always attaches Origin to cross-site
 * POSTs, so requiring it to match the validated public origin — or the
 * Host the request itself carries — rejects forged mutations without a
 * second token round-trip.
 */
export function isSameOrigin(request: Request, allowedOrigins: string[]): boolean {
  const origin = request.headers.get("origin");
  const host = request.headers.get("host");
  if (!origin) return false;
  let parsed: URL;
  try {
    parsed = new URL(origin);
  } catch {
    return false;
  }
  if (allowedOrigins.includes(parsed.origin)) return true;
  // The deployment may legitimately serve under a different advertised host
  // than ANTS_WEB_URL behind a local proxy; matching the request's own Host
  // keeps that honest while still refusing every foreign origin.
  return host !== null && parsed.host === host && (parsed.protocol === "http:" || parsed.protocol === "https:");
}
