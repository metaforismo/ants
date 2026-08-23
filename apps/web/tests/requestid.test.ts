import { describe, expect, it } from "vitest";

import { generateRequestId, resolveRequestId, validRequestId } from "@/lib/requestid";

describe("request id grammar", () => {
  it("accepts the API's correlation grammar", () => {
    expect(validRequestId("req_abc123")).toBe(true);
    expect(validRequestId("web_" + "0".repeat(32))).toBe(true);
    expect(validRequestId("a".repeat(128))).toBe(true);
    expect(validRequestId("x~:y@z-._X")).toBe(true);
  });

  it("refuses malformed or oversized identifiers", () => {
    expect(validRequestId("")).toBe(false);
    expect(validRequestId("a".repeat(129))).toBe(false);
    expect(validRequestId("bad id with spaces")).toBe(false);
    expect(validRequestId("emoji-\u{1F41C}")).toBe(false);
    expect(validRequestId("new\nline")).toBe(false);
  });
});

describe("resolveRequestId", () => {
  it("passes a well-formed browser id through unchanged (end-to-end correlation)", () => {
    const result = resolveRequestId("browser-trace-1");
    expect(result).toEqual({ id: "browser-trace-1", source: "header" });
  });

  it("mints a fresh id when the inbound header is absent or invalid", () => {
    expect(resolveRequestId(null).source).toBe("generated");
    expect(resolveRequestId("").source).toBe("generated");
    expect(resolveRequestId("invalid value!").source).toBe("generated");
    const generated = resolveRequestId(undefined).id;
    expect(generated.startsWith("web_")).toBe(true);
    expect(validRequestId(generated)).toBe(true);
  });

  it("generates unique ids per call", () => {
    expect(generateRequestId()).not.toBe(generateRequestId());
  });
});
