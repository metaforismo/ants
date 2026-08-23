import { describe, expect, it } from "vitest";

import { isSameOrigin, safeRedirectPath } from "@/lib/origin";

describe("safeRedirectPath", () => {
  it("accepts in-app relative paths", () => {
    expect(safeRedirectPath("/threads")).toBe("/threads");
    expect(safeRedirectPath("/threads/abc?x=1")).toBe("/threads/abc?x=1");
  });

  it("refuses off-origin tricks", () => {
    expect(safeRedirectPath("https://evil.example")).toBeUndefined();
    expect(safeRedirectPath("//evil.example")).toBeUndefined();
    expect(safeRedirectPath("/\\evil.example")).toBeUndefined();
    expect(safeRedirectPath("/threads\\..\\..")).toBeUndefined();
    expect(safeRedirectPath("javascript:alert(1)")).toBeUndefined();
  });

  it("refuses control characters and oversized input", () => {
    expect(safeRedirectPath("/threads\r\nSet-Cookie: x")).toBeUndefined();
    expect(safeRedirectPath(`/${"a".repeat(600)}`)).toBeUndefined();
    expect(safeRedirectPath(null)).toBeUndefined();
    expect(safeRedirectPath("")).toBeUndefined();
  });
});

describe("isSameOrigin", () => {
  function requestWith(origin: string | null, host = "console.local:3100"): Request {
    const headers = new Headers();
    if (origin) headers.set("origin", origin);
    headers.set("host", host);
    return new Request("http://console.local:3100/api/v1/projects", { headers });
  }

  it("accepts exactly the validated public origin", () => {
    expect(isSameOrigin(requestWith("http://console.local:3100"), ["http://console.local:3100"])).toBe(true);
  });

  it("refuses cross-site forgeries and missing origins", () => {
    expect(isSameOrigin(requestWith("https://evil.example"), ["http://console.local:3100"])).toBe(false);
    expect(isSameOrigin(requestWith(null), ["http://console.local:3100"])).toBe(false);
    expect(isSameOrigin(requestWith("not a url"), [])).toBe(false);
  });

  it("never accepts a hostile origin merely because the Host header repeats it", () => {
    // Audit regression (DNS-rebinding shape): an attacker controlling both
    // headers can make them agree; only the validated configured origin is
    // ever accepted.
    expect(isSameOrigin(requestWith("https://evil.example", "evil.example"), [])).toBe(false);
    expect(isSameOrigin(requestWith("https://console.local", "advertised.local"), [])).toBe(false);
    expect(isSameOrigin(requestWith("https://evil.example", "evil.example"), ["http://console.local:3100"])).toBe(false);
  });

  it("does not accept foreign schemes or hosts behind any header agreement", () => {
    expect(isSameOrigin(requestWith("ftp://console.local:3100"), [])).toBe(false);
    // A deployment served under a host other than ANTS_WEB_URL must fix its
    // configuration; the console refuses rather than trusting the request's
    // own Host claim.
    expect(isSameOrigin(requestWith("http://advertised:3100", "advertised:3100"), [])).toBe(false);
  });
});
