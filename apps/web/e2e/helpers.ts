import { request, expect, type Page } from "@playwright/test";
import { execFileSync } from "node:child_process";

/**
 * Shared E2E helpers. Everything here talks to the disposable local stack
 * provisioned by scripts/test-web-e2e.sh — never to a shared environment.
 */

export const FIXTURE_USER = "alice";
export const FIXTURE_PASSWORD = "fixture-alice-password";

const KEYCLOAK = process.env.ANTS_E2E_ISSUER ?? "http://127.0.0.1:54331/realms/ants";
const KEYCLOAK_ADMIN = new URL(KEYCLOAK).origin;

/** Drives the real Keycloak login form (Authorization Code + PKCE round trip). */
export async function loginViaKeycloak(page: Page): Promise<void> {
  await page.goto("/login");
  await expect(page.getByTestId("login-button")).toBeVisible();
  await page.getByTestId("login-button").click();

  await page.waitForURL(/realms\/ants\/protocol\/openid-connect\/auth/, {
    timeout: 20_000,
  });
  // Keycloak's login theme labels more than one control with accessible
  // text containing these words; the field ids are the stable contract.
  await page.locator("#username").fill(FIXTURE_USER);
  await page.locator("#password").fill(FIXTURE_PASSWORD);
  await page.getByRole("button", { name: "Sign In" }).click();

  await page.waitForURL(/\/threads$/, { timeout: 30_000 });
}

/** Revokes every server-side session for the fixture user via the admin API. */
export async function revokeKeycloakSessions(username: string): Promise<void> {
  const adminToken = JSON.parse(
    execFileSync(
      "curl",
      [
        "-sf",
        "-X",
        "POST",
        `${KEYCLOAK_ADMIN}/realms/master/protocol/openid-connect/token`,
        "-d",
        "grant_type=password",
        "-d",
        "client_id=admin-cli",
        "-d",
        `username=${process.env.ANTS_E2E_KC_ADMIN ?? "fixture-admin"}`,
        "-d",
        `password=${process.env.ANTS_E2E_KC_PASSWORD ?? "fixture-admin-password"}`,
      ],
      { encoding: "utf8" },
    ),
  ).access_token as string;

  const ctx = await request.newContext({
    baseURL: KEYCLOAK_ADMIN,
    extraHTTPHeaders: { authorization: `Bearer ${adminToken}` },
  });
  const users = await ctx.get(`/admin/realms/ants/users?username=${username}&exact=true`);
  const list = (await users.json()) as Array<{ id: string }>;
  if (!Array.isArray(list) || list.length === 0) {
    throw new Error(`fixture user ${username} not found in Keycloak`);
  }
  const response = await ctx.post(`/admin/realms/ants/users/${list[0]!.id}/logout`);
  if (!response.ok()) {
    throw new Error(`failed to revoke sessions: ${response.status()}`);
  }
  await ctx.dispose();
}

/** Reads the session probe the console itself exposes (identity only). */
export async function sessionProbe(page: Page): Promise<{
  status: string;
  username?: string;
  tokenExpiresAt?: number;
}> {
  return page.evaluate(async () => {
    const response = await fetch("/api/auth/session", { cache: "no-store" });
    return response.json();
  });
}
