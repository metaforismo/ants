import { afterEach, describe, expect, it, vi } from "vitest";

import { OnboardingError, bootstrapTenant } from "@/lib/onboarding";

const API = new URL("http://api.local:8081");

/** Stubs global fetch with scripted (status, body) per call path. */
function scriptFetch(
  handler: (url: string, init?: RequestInit) => { status: number; body?: unknown },
): void {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: string | URL, init?: RequestInit) => {
      const result = handler(String(input), init);
      return new Response(result.body === undefined ? undefined : JSON.stringify(result.body), {
        status: result.status,
        headers: { "content-type": "application/json" },
      });
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("bootstrapTenant", () => {
  it("does nothing when the tenant already resolves", async () => {
    scriptFetch((url) => {
      expect(url).toBe(new URL("/v1/projects", API).toString());
      return { status: 200, body: { projects: [] } };
    });
    await expect(bootstrapTenant(API, "token", "acme")).resolves.toBeUndefined();
  });

  it("creates a missing tenant on the documented open endpoint", async () => {
    const calls: string[] = [];
    let gets = 0;
    scriptFetch((url, init) => {
      calls.push(`${init?.method} ${new URL(url).pathname}`);
      if (init?.method === "GET") {
        gets += 1;
        return gets === 1
          ? { status: 401, body: { code: "unknown_tenant" } }
          : { status: 200, body: { projects: [] } };
      }
      return { status: 201, body: { slug: "acme" } };
    });
    await bootstrapTenant(API, "token", "acme");
    expect(calls).toEqual([
      "GET /v1/projects",
      "POST /v1/tenants",
      "GET /v1/projects",
    ]);
  });

  it("treats a concurrent creation of the same slug as success", async () => {
    let gets = 0;
    scriptFetch((_url, init) => {
      if (init?.method === "GET") {
        gets += 1;
        return gets === 1
          ? { status: 401, body: { code: "unknown_tenant" } }
          : { status: 200, body: { projects: [] } };
      }
      return { status: 409, body: { code: "tenant_slug_taken" } };
    });
    await expect(bootstrapTenant(API, "token", "acme")).resolves.toBeUndefined();
  });

  it("propagates unrelated failures instead of creating tenants blindly", async () => {
    scriptFetch(() => ({ status: 500, body: { code: "store_unavailable" } }));
    await expect(bootstrapTenant(API, "token", "acme")).rejects.toThrow(OnboardingError);
  });

  it("refuses when the tenant is still unresolvable after creation", async () => {
    scriptFetch((_url, init) =>
      init?.method === "GET"
        ? { status: 401, body: { code: "unknown_tenant" } }
        : { status: 201, body: {} },
    );
    await expect(bootstrapTenant(API, "token", "acme")).rejects.toThrow(/still unresolvable/);
  });
});
