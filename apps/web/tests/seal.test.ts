import { describe, expect, it } from "vitest";

import { seal, unseal, SealError } from "@/lib/seal";

const KEY = new Uint8Array(32).fill(7);

describe("seal/unseal", () => {
  it("round-trips plaintext", () => {
    const sealed = seal("hello ants", KEY);
    expect(unseal(sealed, KEY)).toBe("hello ants");
  });

  it("produces distinct ciphertexts for identical plaintext (random IV)", () => {
    expect(seal("same", KEY)).not.toBe(seal("same", KEY));
  });

  it("detects tampering with the ciphertext", () => {
    const sealed = seal("payload", KEY);
    const parts = sealed.split(":");
    const cipher = Buffer.from(parts[2] as string, "base64url");
    cipher[0] = cipher[0]! ^ 1;
    parts[2] = cipher.toString("base64url");
    expect(() => unseal(parts.join(":"), KEY)).toThrow(SealError);
  });

  it("detects a truncated tag", () => {
    const sealed = seal("payload", KEY);
    const parts = sealed.split(":");
    const shortTag = Buffer.from(parts[3] as string, "base64url").subarray(0, 12);
    parts[3] = shortTag.toString("base64url");
    expect(() => unseal(parts.join(":"), KEY)).toThrow(SealError);
  });

  it("rejects foreign keys and malformed formats without leaking which part failed", () => {
    const sealed = seal("payload", KEY);
    const otherKey = new Uint8Array(32).fill(9);
    expect(() => unseal(sealed, otherKey)).toThrow(SealError);
    expect(() => unseal("v2:x:y:z", KEY)).toThrow(SealError);
    expect(() => unseal("", KEY)).toThrow(SealError);
  });

  it("enforces the 32-byte key size", () => {
    expect(() => seal("x", new Uint8Array(16))).toThrow(/32 bytes/);
    expect(() => unseal("v1:a:b:c", new Uint8Array(31))).toThrow(/32 bytes/);
  });
});
