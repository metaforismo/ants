import { expect, test } from "@playwright/test";

import { loginViaKeycloak, FIXTURE_USER } from "./helpers";
import { randomUUID } from "node:crypto";

test.describe("operate journey", () => {
  test("login → project → thread → message → run → terminal report", async ({ page }) => {
    test.setTimeout(240_000);
    await loginViaKeycloak(page);

    // The rail proves the server-side session identity without tokens.
    await expect(page.getByText(FIXTURE_USER)).toBeVisible();
    await expect(page.getByText("acme", { exact: true })).toBeVisible();

    const suffix = randomUUID().slice(0, 8);

    // --- create the first project inside the new-thread dialog ---
    await page.getByTestId("new-thread").click();
    await page.getByLabel("Project name").fill(`E2E project ${suffix}`);
    await page.getByLabel("Slug").fill(`e2e-${suffix}`);
    await page.getByRole("button", { name: "Create project" }).click();
    await expect(page.getByLabel("Outcome")).toBeVisible();

    await page
      .getByLabel("Outcome")
      .fill(`Add and multiply integers for e2e-${suffix}`);
    await page.getByRole("button", { name: "Open thread" }).click();

    // --- workspace opens on the fresh thread ---
    await expect(page.getByRole("heading", { name: /add and multiply/i })).toBeVisible({
      timeout: 20_000,
    });
    await expect(page.getByTestId("messages-empty")).toContainText("No messages yet");

    // --- describe the outcome ---
    await page
      .getByTestId("composer")
      .fill(`Please add and multiply integers for e2e-${suffix}.`);
    await page.getByTestId("send-message").click();
    await expect(page.getByTestId("message-list")).toContainText("add and multiply integers", {
      timeout: 15_000,
    });

    // --- start the run; the panel anchors to its id ---
    await page.getByTestId("start-run").click();
    await expect(page.getByTestId("run-panel")).toBeVisible({ timeout: 20_000 });

    // --- observe to terminal with live events and task states ---
    await expect(page.getByTestId("run-panel").getByText(/completed|failed/)).toBeVisible({
      timeout: 180_000,
    });
    await expect(page.getByTestId("event-trail")).toBeVisible();

    // --- evidence-first terminal report ---
    const report = page.getByTestId("run-report");
    await expect(report).toBeVisible({ timeout: 30_000 });
    await expect(report.getByText("Verification")).toBeVisible();
    await expect(report.locator(".evidence-table tbody tr").first()).toBeVisible();
    await expect(report.getByText(/passed|failed/).first()).toBeVisible();
  });
});
