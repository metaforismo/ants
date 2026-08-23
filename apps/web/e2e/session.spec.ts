import { expect, test } from "@playwright/test";

import {
  loginViaKeycloak,
  revokeKeycloakSessions,
  sessionProbe,
  FIXTURE_USER,
} from "./helpers";

test.describe("session security states", () => {
  test("anonymous console access is redirected to the login surface", async ({ page }) => {
    await page.goto("/threads");
    await expect(page).toHaveURL(/\/login/);
    await expect(page.getByTestId("login-button")).toBeVisible();
  });

  test("the BFF refuses unauthenticated API calls with a typed problem", async ({ request }) => {
    const response = await request.get("/api/v1/projects");
    expect(response.status()).toBe(401);
    const body = (await response.json()) as { code?: string };
    expect(body.code).toBe("session_expired");
  });

  test("a tampered session cookie yields the re-authentication state, never partial data", async ({ page }) => {
    await page.context().addCookies([
      {
        name: "ants_session",
        value: "v1:AAAA:BBBB:CCCC",
        url: "http://127.0.0.1:3100",
      },
    ]);
    await page.goto("/threads");
    // The server-side gate bounces an unusable session to login.
    await expect(page).toHaveURL(/\/login/);
  });
});

test.describe("token lifecycle against the real provider", () => {
  test("expired access tokens renew silently through the BFF", async ({ page }) => {
    test.setTimeout(240_000);
    await loginViaKeycloak(page);

    const first = await sessionProbe(page);
    expect(first.status).toBe("authenticated");
    const firstExpiry = first.tokenExpiresAt!;
    expect(firstExpiry).toBeGreaterThan(Math.floor(Date.now() / 1000));

    // Fixture client lifespan is 60s; wait past it while polling. A renewal
    // is proven when the advertised expiry jumps forward without any
    // interactive step.
    await expect
      .poll(
        async () => {
          const probe = await sessionProbe(page);
          return probe.status === "authenticated" ? probe.tokenExpiresAt ?? 0 : 0;
        },
        { timeout: 150_000, intervals: [5_000] },
      )
      .toBeGreaterThan(firstExpiry + 30);

    // The renewed session still operates the API transparently.
    await expect(page.getByTestId("new-thread")).toBeVisible();
  });

  test("a revoked provider session degrades to the re-authentication card", async ({ page }) => {
    test.setTimeout(300_000);
    await loginViaKeycloak(page);
    await expect(page.getByTestId("new-thread")).toBeVisible();

    await revokeKeycloakSessions(FIXTURE_USER);

    // After the current access token lapses, refresh must fail with
    // invalid_grant; the console then shows the re-auth state instead of
    // retrying forever.
    await expect
      .poll(
        async () =>
          page.evaluate(async () => {
            const response = await fetch("/api/v1/projects", { cache: "no-store" });
            return response.status === 401
              ? ((await response.json()) as { code?: string }).code ?? ""
              : `status-${response.status}`;
          }),
        { timeout: 200_000, intervals: [5_000] },
      )
      .toBe("session_expired");

    await page.reload();
    await expect(page.getByText(/sign in again/i).first()).toBeVisible();
  });
});
