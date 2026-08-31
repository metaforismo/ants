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

    // --- start the run; the workspace discovers it from the run history ---
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

    // The real run-history component remains operable at the narrow layout:
    // the exact id moves to the selected panel and the document never overflows.
    await page.setViewportSize({ width: 390, height: 844 });
    const history = page.getByTestId("run-history");
    await expect(history).toBeVisible();
    await expect(history.locator(".run-row-id")).toBeHidden();

    const layout = await page.evaluate(() => {
      const viewportWidth = document.documentElement.clientWidth;
      const describe = (name: string, element: HTMLElement | null) => {
        if (!element) return { name, missing: true };
        const rect = element.getBoundingClientRect();
        const style = getComputedStyle(element);
        return {
          name,
          missing: false,
          tag: element.tagName.toLowerCase(),
          id: element.id,
          className: typeof element.className === "string" ? element.className : "",
          left: Math.round(rect.left * 10) / 10,
          right: Math.round(rect.right * 10) / 10,
          width: Math.round(rect.width * 10) / 10,
          clientWidth: element.clientWidth,
          scrollWidth: element.scrollWidth,
          display: style.display,
          position: style.position,
          boxSizing: style.boxSizing,
          computedWidth: style.width,
          minWidth: style.minWidth,
          maxWidth: style.maxWidth,
          paddingLeft: style.paddingLeft,
          paddingRight: style.paddingRight,
          overflowX: style.overflowX,
          whiteSpace: style.whiteSpace,
          gridTemplateColumns: style.gridTemplateColumns,
          flexBasis: style.flexBasis,
          text: (element.textContent ?? "").trim().replace(/\s+/g, " ").slice(0, 80),
        };
      };
      const offenders = Array.from(document.body.querySelectorAll<HTMLElement>("*"))
        .map((element) => describe("offender", element))
        .filter(
          (entry) =>
            !entry.missing &&
            ((entry.left ?? 0) < -1 || (entry.right ?? 0) > viewportWidth + 1),
        )
        .sort((left, right) => (right.right ?? 0) - (left.right ?? 0))
        .slice(0, 12);
      const roots = [
        describe("html", document.documentElement),
        describe("body", document.body),
        describe("shell", document.querySelector<HTMLElement>(".shell")),
        describe("rail", document.querySelector<HTMLElement>(".rail")),
        describe("main", document.querySelector<HTMLElement>("#main")),
        describe("workspace", document.querySelector<HTMLElement>("#main > div")),
        describe("conversation", document.querySelector<HTMLElement>('[aria-label="Conversation"]')),
        describe("composer", document.querySelector<HTMLElement>(".composer")),
        describe("composer-field", document.querySelector<HTMLElement>(".composer > div")),
        describe("composer-input", document.querySelector<HTMLElement>("#composer-input")),
        describe(
          "evidence-scroll",
          document.querySelector<HTMLElement>('[data-testid="verification-evidence-scroll"]'),
        ),
        describe("evidence-table", document.querySelector<HTMLElement>(".evidence-table")),
      ];

      return {
        overflow: document.documentElement.scrollWidth - viewportWidth,
        viewportWidth,
        roots,
        offenders,
      };
    });

    expect(
      layout.overflow,
      `horizontal overflow roots=${JSON.stringify(layout.roots)} offenders=${JSON.stringify(layout.offenders)}`,
    ).toBeLessThanOrEqual(1);
  });
});
