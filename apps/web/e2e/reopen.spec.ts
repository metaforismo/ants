import { expect, test } from "@playwright/test";
import { randomUUID } from "node:crypto";

import { loginViaKeycloak } from "./helpers";

test.describe("reopen thread reattaches", () => {
  // Reattachment must come from the server's run history, not from any
  // per-tab anchor: sessionStorage survives same-tab navigation, so the
  // honest proof is a SECOND TAB on the thread URL — tabs never share
  // sessionStorage, yet the live/latest run must be discovered there.
  test("a reopened thread discovers its live/latest run in another tab", async ({ page }) => {
    test.setTimeout(300_000);
    await loginViaKeycloak(page);

    const suffix = randomUUID().slice(0, 8);

    // --- create the project inside the new-thread dialog ---
    await page.getByTestId("new-thread").click();
    await page.getByLabel("Project name").fill(`Reopen project ${suffix}`);
    await page.getByLabel("Slug").fill(`e2e-reopen-${suffix}`);
    await page.getByRole("button", { name: "Create project" }).click();
    await expect(page.getByLabel("Outcome")).toBeVisible();

    // --- open the thread and describe the outcome ---
    await page
      .getByLabel("Outcome")
      .fill(`Add and multiply integers for e2e-reopen-${suffix}`);
    await page.getByRole("button", { name: "Open thread" }).click();
    await expect(page.getByRole("heading", { name: /add and multiply/i })).toBeVisible({
      timeout: 20_000,
    });
    await page
      .getByTestId("composer")
      .fill(`Please add and multiply integers for e2e-reopen-${suffix}.`);
    await page.getByTestId("send-message").click();
    await expect(page.getByTestId("message-list")).toContainText("add and multiply integers", {
      timeout: 15_000,
    });

    // --- start the run and note its identity ---
    await page.getByTestId("start-run").click();
    await expect(page.getByTestId("run-panel")).toBeVisible({ timeout: 20_000 });
    const runId = (await page.getByTestId("run-panel").locator(".mono").first().textContent())?.trim();
    expect(runId ?? "").toMatch(/^run_/);
    const threadUrl = page.url();

    // --- a second tab reopens the same thread cold ---
    const secondTab = await page.context().newPage();
    await secondTab.goto(threadUrl);

    // The panel reattaches without any start action in this tab.
    await expect(secondTab.getByTestId("run-panel")).toBeVisible({ timeout: 20_000 });
    await expect(secondTab.getByTestId("run-panel").locator(".mono").first()).toHaveText(runId!);

    // Full run-detail parity holds in the reopened tab: live events, then
    // the evidence-first terminal report.
    await expect(secondTab.getByTestId("run-panel").getByText(/completed|failed/)).toBeVisible({
      timeout: 180_000,
    });
    await expect(secondTab.getByTestId("event-trail")).toBeVisible();
    const report = secondTab.getByTestId("run-report");
    await expect(report).toBeVisible({ timeout: 30_000 });
    await expect(report.locator(".evidence-table tbody tr").first()).toBeVisible();

    // The originating tab converges on the same terminal truth.
    await expect(page.getByTestId("run-panel").getByText(/completed|failed/)).toBeVisible({
      timeout: 60_000,
    });

    await secondTab.close();
  });
});
