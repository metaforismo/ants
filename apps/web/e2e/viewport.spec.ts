import { expect, test } from "@playwright/test";

import { loginViaKeycloak } from "./helpers";

test.describe("responsive and motion accessibility", () => {
  test("the console operates at a mobile viewport with the stacked layout", async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await loginViaKeycloak(page);

    // Rail becomes a top bar; primary content stays reachable.
    const rail = page.locator("nav.rail");
    await expect(rail).toBeVisible();
    const railBox = await rail.boundingBox();
    expect(railBox!.width).toBeGreaterThan(300);
    await expect(page.getByTestId("new-thread")).toBeVisible();

    // No horizontal overflow at 390px.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth - document.documentElement.clientWidth,
    );
    expect(overflow).toBeLessThanOrEqual(1);
  });

  test("keyboard-only operation reaches the login entry point", async ({ page }) => {
    await page.goto("/login");
    await page.keyboard.press("Tab"); // skip link
    await expect(page.getByText("Skip to content")).toBeFocused();
    await page.keyboard.press("Tab"); // login button
    await expect(page.getByTestId("login-button")).toBeFocused();
    await page.keyboard.press("Enter");
    await expect(page).toHaveURL(/realms\/ants/);
  });

  test("reduced motion disables the pulse and trail animations", async ({ page }) => {
    await page.emulateMedia({ reducedMotion: "reduce" });
    await page.goto("/login");

    const result = await page.evaluate(() => {
      // Probe the actual stylesheet behavior: a live-run glyph and a trail
      // entry must both lose their keyframe animation under reduce.
      const probe = (attrs: Array<[string, string]>, cls?: string) => {
        const el = document.createElement("span");
        for (const [name, value] of attrs) el.setAttribute(name, value);
        if (cls) el.className = cls;
        document.body.appendChild(el);
        return getComputedStyle(el).animationName;
      };
      return {
        prefersReduce: window.matchMedia("(prefers-reduced-motion: reduce)").matches,
        pulse: probe([["data-motion", "pulse"]]),
        trail: probe([], "trail-in"),
      };
    });

    expect(result.prefersReduce).toBe(true);
    expect(result.pulse).toBe("none");
    expect(result.trail).not.toBe("trail-in");
  });
});
