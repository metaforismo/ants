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
 * POSTs, so requiring it to equal the validated public origin rejects
 * forged mutations without a second token round-trip. Acceptance is keyed
 * exclusively to the configured origin — never to the request's own Host,
 * which an attacker controlling both headers (DNS rebinding) can match at
 * will. Legitimate reachability of that origin is already guaranteed: the
 * Authorization Code flow's redirect_uri is derived from ANTS_WEB_URL and
 * matched byte-exactly by the provider, so every user who completed login
 * was serving from exactly this origin.
 */
export function isSameOrigin(request: Request, allowedOrigins: string[]): boolean {
  const origin = request.headers.get("origin");
  if (!origin) return false;
  let parsed: URL;
  try {
    parsed = new URL(origin);
  } catch {
    return false;
  }
  return allowedOrigins.includes(parsed.origin);
}
