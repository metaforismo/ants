import { describe, expect, it } from "vitest";

import { parseWebConfig, ConfigError, SESSION_COOKIE, AUTH_TX_COOKIE } from "@/lib/config";

const VALID_ENV = {
  ANTS_WEB_URL: "https://console.example.com",
  ANTS_API_BASE_URL: "https://api.example.com",
  ANTS_OIDC_ISSUER_URL: "https://idp.example.com/realms/ants",
  ANTS_OIDC_CLIENT_ID: "ants-web",
  ANTS_SESSION_KEY: Buffer.alloc(32, 3).toString("base64"),
};

describe("parseWebConfig", () => {
  it("parses a complete environment and derives secure cookies from https", () => {
    const cfg = parseWebConfig(VALID_ENV);
    expect(cfg.webUrl.origin).toBe("https://console.example.com");
    expect(cfg.apiBaseUrl.pathname).toBe("/");
    expect(cfg.scopes).toBe("openid");
    expect(cfg.tenantClaim).toBe("ants_tenant");
    expect(cfg.secureCookies).toBe(true);
    expect(cfg.sessionKey).toHaveLength(32);
  });

  it("derives non-secure cookies for loopback http fixtures", () => {
    const cfg = parseWebConfig({ ...VALID_ENV, ANTS_WEB_URL: "http://127.0.0.1:3100" });
    expect(cfg.secureCookies).toBe(false);
  });

  it("collects every problem in one failure", () => {
    try {
      parseWebConfig({});
      expect.unreachable("config must fail closed");
    } catch (err) {
      expect(err).toBeInstanceOf(ConfigError);
      const problems = (err as ConfigError).problems;
      expect(problems.some((p) => p.includes("ANTS_WEB_URL"))).toBe(true);
      expect(problems.some((p) => p.includes("ANTS_API_BASE_URL"))).toBe(true);
      expect(problems.some((p) => p.includes("ANTS_OIDC_ISSUER_URL"))).toBe(true);
      expect(problems.some((p) => p.includes("ANTS_OIDC_CLIENT_ID"))).toBe(true);
      expect(problems.some((p) => p.includes("ANTS_SESSION_KEY"))).toBe(true);
    }
  });

  it("rejects issuer URLs that are neither https nor literal loopback", () => {
    expect(() =>
      parseWebConfig({
        ...VALID_ENV,
        ANTS_OIDC_ISSUER_URL: "http://idp.example.com/realms/ants",
      }),
    ).toThrow(/loopback/);
    expect(() =>
      parseWebConfig({
        ...VALID_ENV,
        ANTS_OIDC_ISSUER_URL: "http://localhost:54331/realms/ants",
      }),
    ).not.toThrow();
  });

  it("rejects base URLs with path components and wrong schemes", () => {
    expect(() =>
      parseWebConfig({ ...VALID_ENV, ANTS_API_BASE_URL: "https://api.example.com/v1/" }),
    ).toThrow(/origin without a path/);
    expect(() =>
      parseWebConfig({ ...VALID_ENV, ANTS_WEB_URL: "ftp://console.example.com" }),
    ).toThrow(/http or https/);
  });

  it("enforces the exact 32-byte session key", () => {
    const shortKey = { ...VALID_ENV, ANTS_SESSION_KEY: Buffer.alloc(16).toString("base64") };
    expect(() => parseWebConfig(shortKey)).toThrow(/exactly 32 bytes/);
    const notBase64 = { ...VALID_ENV, ANTS_SESSION_KEY: "!!!not base64!!!" };
    expect(() => parseWebConfig(notBase64)).toThrow();
  });
});

describe("cookie names", () => {
  it("keeps stable cookie names so operators can scope policies", () => {
    expect(SESSION_COOKIE).toBe("ants_session");
    expect(AUTH_TX_COOKIE).toBe("ants_auth_tx");
  });
});
