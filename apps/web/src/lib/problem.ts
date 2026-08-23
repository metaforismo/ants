import type { components } from "@ants/contracts";

/**
 * RFC 9457 Problem Details as the /v1 API renders them, plus the stable
 * `code` extension the whole Ants error taxonomy is built on. The BFF maps
 * every non-2xx API response onto this shape; the browser never sees raw
 * provider text. Runtime parsing lives in lib/client-api.ts, which owns
 * the only code path that turns responses into these values.
 */
export type Problem = components["schemas"]["Problem"];
