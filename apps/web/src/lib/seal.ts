import { createCipheriv, createDecipheriv, randomBytes } from "node:crypto";

/**
 * Authenticated AES-256-GCM sealing for session cookies. The sealed payload
 * is tamper-evident: any modification (including bit flips and truncation)
 * fails authentication and yields an unusable session, never a partially
 * trusted one.
 *
 * Format: v1:<iv-b64url>:<ciphertext+b64url>:<tag-b64url>
 */
const V1 = "v1";
const IV_BYTES = 12;
const KEY_BYTES = 32;
const TAG_BYTES = 16;

export class SealError extends Error {
  constructor() {
    super("sealed value failed authentication");
    this.name = "SealError";
  }
}

function assertKey(key: Uint8Array): void {
  if (key.length !== KEY_BYTES) {
    throw new Error(`seal key must be ${KEY_BYTES} bytes`);
  }
}

export function seal(plaintext: string, key: Uint8Array): string {
  assertKey(key);
  const iv = randomBytes(IV_BYTES);
  // Pinning authTagLength on both directions makes Node refuse tags of any
  // other size instead of historically comparing a truncated tag prefix.
  const cipher = createCipheriv("aes-256-gcm", key, iv, { authTagLength: TAG_BYTES });
  const ciphertext = Buffer.concat([cipher.update(plaintext, "utf8"), cipher.final()]);
  const tag = cipher.getAuthTag();
  return [V1, iv.toString("base64url"), ciphertext.toString("base64url"), tag.toString("base64url")].join(":");
}

export function unseal(sealed: string, key: Uint8Array): string {
  assertKey(key);
  const parts = sealed.split(":");
  if (parts[0] !== V1 || parts.length !== 4) {
    throw new SealError();
  }
  try {
    const iv = Buffer.from(parts[1] as string, "base64url");
    if (iv.length !== IV_BYTES) throw new SealError();
    const tag = Buffer.from(parts[3] as string, "base64url");
    if (tag.length !== TAG_BYTES) throw new SealError();
    const decipher = createDecipheriv("aes-256-gcm", key, iv, { authTagLength: TAG_BYTES });
    decipher.setAuthTag(tag);
    return Buffer.concat([
      decipher.update(Buffer.from(parts[2] as string, "base64url")),
      decipher.final(),
    ]).toString("utf8");
  } catch (err) {
    if (err instanceof SealError) throw err;
    // Authentication failure must be indistinguishable from format garbage:
    // no oracle about which part failed.
    throw new SealError();
  }
}
