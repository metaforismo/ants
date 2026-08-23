import type { Problem } from "@/lib/problem";

/**
 * Onboarding: the tenant named by the verified token must exist before any
 * authenticated /v1 call succeeds (the API resolves the tenant claim against
 * the store on every request and refuses unknown tenants). Until memberships
 * ship, `POST /v1/tenants` is the documented open bootstrap endpoint, so the
 * first login of a claimed tenant creates it. Everything else is an error.
 */

export class OnboardingError extends Error {
  constructor(message: string) {
    super(`tenant bootstrap failed: ${message}`);
    this.name = "OnboardingError";
  }
}

async function callApi(
  apiBaseUrl: URL,
  path: string,
  init: { method: string; body?: unknown; token?: string },
): Promise<{ status: number; problem?: Problem }> {
  const response = await fetch(new URL(path, apiBaseUrl), {
    method: init.method,
    headers: {
      "content-type": "application/json",
      ...(init.token ? { authorization: `Bearer ${init.token}` } : {}),
    },
    body: init.body === undefined ? undefined : JSON.stringify(init.body),
    cache: "no-store",
  });
  if (response.ok) return { status: response.status };
  let problem: Problem | undefined;
  try {
    const body = (await response.json()) as Partial<Problem>;
    if (body && typeof body.code === "string") {
      problem = { ...body } as Problem;
    }
  } catch {
    // Non-JSON failure body: fall through to a generic refusal below.
  }
  return { status: response.status, problem };
}

export async function bootstrapTenant(
  apiBaseUrl: URL,
  accessToken: string,
  tenantSlug: string,
): Promise<void> {
  // Probe with the authenticated caller; unknown_tenant means bootstrap is
  // needed. Anything else (including transient outages) propagates.
  const probe = await callApi(apiBaseUrl, "/v1/projects", { method: "GET", token: accessToken });
  if (probe.status < 300) return;
  if (!(probe.status === 401 && probe.problem?.code === "unknown_tenant")) {
    throw new OnboardingError(
      probe.problem ? `${probe.status} ${probe.problem.code}` : `${probe.status} response`,
    );
  }

  const created = await callApi(apiBaseUrl, "/v1/tenants", {
    method: "POST",
    body: { slug: tenantSlug, name: tenantSlug },
  });
  if (created.status >= 300 && created.problem?.code !== "tenant_slug_taken") {
    // A concurrent first login may already have created the slug; that is
    // success for our purposes. Any other refusal is fatal to onboarding.
    throw new OnboardingError(
      created.problem ? `${created.status} ${created.problem.code}` : `${created.status} response`,
    );
  }

  const retry = await callApi(apiBaseUrl, "/v1/projects", { method: "GET", token: accessToken });
  if (retry.status >= 300) {
    throw new OnboardingError("tenant still unresolvable after creation");
  }
}
