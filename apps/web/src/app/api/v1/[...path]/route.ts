import { NextResponse } from "next/server";

import { getWebConfig } from "@/lib/config";
import type { Problem } from "@/lib/problem";
import { isSameOrigin } from "@/lib/origin";
import { REQUEST_ID_HEADER, resolveRequestId } from "@/lib/requestid";
import { loadFreshSession, SessionExpiredError, clearSession } from "@/lib/session";

export const dynamic = "force-dynamic";

/**
 * The BFF proxy is the only path from the browser to /v1. It attaches the
 * session's bearer token server-side, propagates correlation ids
 * (browser → BFF → API), and maps API failures onto typed problems the UI
 * can classify. Idempotency keys for mutations are minted by the client per
 * logical intent and forwarded verbatim.
 */

const HOP_BY_HOP = new Set([
  "connection",
  "keep-alive",
  "transfer-encoding",
  "upgrade",
  "host",
  "content-length",
]);

type RouteContext = { params: Promise<{ path?: string[] }> };

async function handle(request: Request, context: RouteContext): Promise<Response> {
  const { path } = await context.params;
  const cfg = getWebConfig();

  if (request.method !== "GET" && !isSameOrigin(request, [cfg.webUrl.origin])) {
    return problemResponse(403, {
      type: "about:blank",
      code: "csrf_rejected",
      title: "Cross-origin request refused",
      status: 403,
    });
  }

  let token: string;
  try {
    const loaded = await loadFreshSession();
    token = loaded.session.accessToken;
  } catch (err) {
    if (err instanceof SessionExpiredError) {
      await clearSession();
      return problemResponse(401, {
        type: "about:blank",
        code: "session_expired",
        title: "Your session has expired",
        status: 401,
        detail: "Sign in again to continue.",
      });
    }
    console.error("ants-web: proxy session load failed", err instanceof Error ? err.message : err);
    return problemResponse(503, {
      type: "about:blank",
      code: "web_session_unavailable",
      title: "The web session could not be read",
      status: 503,
    });
  }

  // Re-encode every segment so a crafted path can never walk off /v1/*.
  const subpath = (path ?? []).map(encodeURIComponent).join("/");
  const target = new URL(`/v1/${subpath}${new URL(request.url).search}`, cfg.apiBaseUrl);

  // Correlation: accept a grammar-valid browser id, otherwise mint one; the
  // effective value flows to the API so response header, request log, event
  // trace ids, and audit records stay joinable end to end (ADR-0017/0018).
  const requestId = resolveRequestId(request.headers.get(REQUEST_ID_HEADER)).id;

  const headers = new Headers();
  for (const [name, value] of request.headers.entries()) {
    const lower = name.toLowerCase();
    if (
      HOP_BY_HOP.has(lower) ||
      lower === "cookie" ||
      lower === REQUEST_ID_HEADER.toLowerCase() ||
      (lower === "idempotency-key" && !validIdempotencyKey(value))
    ) {
      continue;
    }
    headers.set(name, value);
  }
  headers.set("authorization", `Bearer ${token}`);
  headers.set(REQUEST_ID_HEADER, requestId);

  let upstream: Response;
  try {
    upstream = await fetch(target, {
      method: request.method,
      headers,
      body:
        request.method === "GET" || request.method === "HEAD"
          ? undefined
          : await request.arrayBuffer(),
      cache: "no-store",
      redirect: "error",
    });
  } catch (err) {
    console.error("ants-web: api unreachable", err instanceof Error ? err.message : err);
    return problemResponse(502, {
      type: "about:blank",
      code: "api_unreachable",
      title: "The Ants API is unreachable",
      status: 502,
      detail: "Requests will resume automatically once it returns.",
    });
  }

  const responseHeaders = new Headers();
  for (const [name, value] of upstream.headers.entries()) {
    if (!HOP_BY_HOP.has(name.toLowerCase()) && name.toLowerCase() !== "set-cookie") {
      responseHeaders.set(name, value);
    }
  }
  return new Response(upstream.body, { status: upstream.status, headers: responseHeaders });
}

export const GET = handle;
export const POST = handle;
export const PUT = handle;
export const PATCH = handle;
export const DELETE = handle;

function validIdempotencyKey(value: string): boolean {
  return value.length > 0 && value.length <= 256;
}

function problemResponse(status: number, problem: Problem): Response {
  return NextResponse.json(problem, { status });
}
