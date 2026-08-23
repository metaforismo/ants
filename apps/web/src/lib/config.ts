/**
 * Typed, fail-closed configuration for the web console. Every value comes
 * from the environment at server runtime; nothing about tenants, issuers,
 * callbacks, or secrets is hardcoded. All problems are collected before a
 * single error is thrown so operators fix configuration in one round.
 *
 * The issuer transport rule mirrors ADR-0019: https everywhere, plaintext
 * allowed only for literal loopback hosts so local Keycloak stays testable
 * without weakening production posture.
 */
export const SESSION_COOKIE = "ants_session";
export const AUTH_TX_COOKIE = "ants_auth_tx";

const LOOPBACK_HOSTS = new Set(["127.0.0.1", "::1", "[::1]", "localhost"]);

export type WebConfig = {
  /** Public origin of this web app (redirect URIs are derived from it). */
  webUrl: URL;
  /** Root of the /v1 API the BFF forwards to. */
  apiBaseUrl: URL;
  issuerUrl: string;
  clientId: string;
  scopes: string;
  /** Verified access-token claim naming the Ants tenant (API default). */
  tenantClaim: string;
  /** 32-byte AES-256-GCM key sealing session cookies. Never serialized. */
  sessionKey: Uint8Array;
  /**
   * Cookie Secure flag. Derived from the public origin's scheme; loopback
   * HTTP fixtures get non-secure cookies because browsers refuse Secure
   * cookies over plain HTTP.
   */
  secureCookies: boolean;
};

export class ConfigError extends Error {
  readonly problems: readonly string[];
  constructor(problems: readonly string[]) {
    super(`invalid web configuration:\n  - ${problems.join("\n  - ")}`);
    this.name = "ConfigError";
    this.problems = problems;
  }
}

function requireValue(
  env: Record<string, string | undefined>,
  name: string,
  problems: string[],
): string {
  const value = env[name]?.trim();
  if (!value) {
    problems.push(`${name} is required`);
    return "";
  }
  return value;
}

function parseUrl(
  raw: string,
  name: string,
  problems: string[],
): URL | undefined {
  try {
    const url = new URL(raw);
    if (url.protocol !== "https:" && url.protocol !== "http:") {
      problems.push(`${name} must use http or https`);
      return undefined;
    }
    if (url.pathname !== "/" && url.pathname !== "") {
      problems.push(`${name} must be an origin without a path component`);
      return undefined;
    }
    return url;
  } catch {
    problems.push(`${name} is not a valid URL: ${raw}`);
    return undefined;
  }
}

function parseIssuer(raw: string, problems: string[]): string | undefined {
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    problems.push(`ANTS_OIDC_ISSUER_URL is not a valid URL: ${raw}`);
    return undefined;
  }
  const loopback =
    url.protocol === "http:" && LOOPBACK_HOSTS.has(url.hostname.toLowerCase());
  if (url.protocol !== "https:" && !loopback) {
    problems.push(
      "ANTS_OIDC_ISSUER_URL must use https unless it serves from literal loopback (mirrors the API transport rule)",
    );
    return undefined;
  }
  // openid-client requires an issuer WITHOUT a trailing .well-known suffix
  // but tolerates realm paths; strip nothing, just normalize no trailing slash.
  return raw.replace(/\/+$/, "");
}

function decodeSessionKey(
  raw: string,
  problems: string[],
): Uint8Array | undefined {
  let bytes: Buffer;
  try {
    bytes = Buffer.from(raw, "base64");
  } catch {
    problems.push("ANTS_SESSION_KEY is not valid base64");
    return undefined;
  }
  if (bytes.length !== 32) {
    problems.push(
      `ANTS_SESSION_KEY must decode to exactly 32 bytes (got ${bytes.length}); generate one with: openssl rand -base64 32`,
    );
    return undefined;
  }
  return new Uint8Array(bytes);
}

/** Pure parser so tests can drive every failure mode deterministically. */
export function parseWebConfig(
  env: Record<string, string | undefined>,
): WebConfig {
  const problems: string[] = [];

  const webUrl = parseUrl(requireValue(env, "ANTS_WEB_URL", problems), "ANTS_WEB_URL", problems);
  const apiBaseUrl = parseUrl(
    requireValue(env, "ANTS_API_BASE_URL", problems),
    "ANTS_API_BASE_URL",
    problems,
  );
  const issuerUrl = parseIssuer(requireValue(env, "ANTS_OIDC_ISSUER_URL", problems), problems);
  const clientId = requireValue(env, "ANTS_OIDC_CLIENT_ID", problems);
  const scopes = env["ANTS_OIDC_SCOPES"]?.trim() || "openid";
  const tenantClaim = env["ANTS_OIDC_TENANT_CLAIM"]?.trim() || "ants_tenant";
  const sessionKey = decodeSessionKey(
    requireValue(env, "ANTS_SESSION_KEY", problems),
    problems,
  );

  if (problems.length > 0) {
    throw new ConfigError(problems);
  }
  return {
    webUrl: webUrl as URL,
    apiBaseUrl: apiBaseUrl as URL,
    issuerUrl: issuerUrl as string,
    clientId,
    scopes,
    tenantClaim,
    sessionKey: sessionKey as Uint8Array,
    secureCookies: (webUrl as URL).protocol === "https:",
  };
}

let cached: WebConfig | undefined;

/**
 * Process-wide validated config. Validated once; a misconfigured deployment
 * fails on first use with every problem listed instead of limping along.
 */
export function getWebConfig(): WebConfig {
  cached ??= parseWebConfig(process.env);
  return cached;
}
