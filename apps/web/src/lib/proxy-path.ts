/**
 * Path-safety for the BFF proxy: the browser may reach /v1/* and nothing
 * else. Next decodes catch-all parameters before handlers run, so a segment
 * may arrive as "." or ".."; dot segments survive encodeURIComponent intact
 * and URL normalization would then resolve them above /v1 on the API origin.
 * Refusing them here makes the containment structural instead of relying on
 * framework routing behavior.
 */
const MAX_SEGMENTS = 16;
const MAX_SEGMENT_LENGTH = 256;

/** Returns proxy-safe segments, or undefined when the path must be refused. */
export function safeProxySegments(
  path: readonly string[] | undefined,
): string[] | undefined {
  if (!path || path.length === 0 || path.length > MAX_SEGMENTS) return undefined;
  for (const segment of path) {
    // Empty segments come from repeated slashes; "." and ".." are traversal
    // regardless of encoding because percent-encoded dots decode back to
    // dots before this point.
    if (segment.length === 0 || segment.length > MAX_SEGMENT_LENGTH) return undefined;
    if (segment === "." || segment === "..") return undefined;
    // A decoded separator can only come from %2F/%5C smuggling inside one
    // raw segment; forwarding it would hand upstream a second path boundary
    // hidden inside a value this layer treats as opaque.
    if (segment.includes("/") || segment.includes("\\")) return undefined;
  }
  return [...path];
}
