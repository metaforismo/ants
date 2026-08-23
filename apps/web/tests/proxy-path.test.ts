import { describe, expect, it } from "vitest";

import { safeProxySegments } from "@/lib/proxy-path";

describe("safeProxySegments", () => {
  it("accepts ordinary /v1 paths and preserves order and values", () => {
    expect(safeProxySegments(["projects"])).toEqual(["projects"]);
    expect(safeProxySegments(["threads", "thr_abc", "messages"])).toEqual([
      "threads",
      "thr_abc",
      "messages",
    ]);
    // Percent-decoded characters that stay within one segment are legal.
    expect(safeProxySegments(["thr_abc def"])).toEqual(["thr_abc def"]);
  });

  it("refuses traversal regardless of how it decodes", () => {
    expect(safeProxySegments([".."])).toBeUndefined();
    expect(safeProxySegments(["."])).toBeUndefined();
    expect(safeProxySegments(["..", "healthz"])).toBeUndefined();
    expect(safeProxySegments(["x", "..", "y"])).toBeUndefined();
  });

  it("refuses separators smuggled inside a single segment (%2F/%5C)", () => {
    // "%2e%2e%2fhealthz" reaches the handler as ONE decoded "../healthz".
    expect(safeProxySegments(["../healthz"])).toBeUndefined();
    expect(safeProxySegments(["threads/x"])).toBeUndefined(); // decoded %2F
    expect(safeProxySegments(["a\\..\\b"])).toBeUndefined();
  });

  it("refuses empty and oversized segments and paths", () => {
    expect(safeProxySegments(undefined)).toBeUndefined();
    expect(safeProxySegments([])).toBeUndefined();
    expect(safeProxySegments([""])).toBeUndefined();
    expect(safeProxySegments(["threads", ""])).toBeUndefined();
    expect(safeProxySegments([`${"a".repeat(257)}`])).toBeUndefined();
    expect(
      safeProxySegments(Array.from({ length: 17 }, (_, i) => `s${i}`)),
    ).toBeUndefined();
  });
});
