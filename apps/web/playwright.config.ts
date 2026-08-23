import { defineConfig } from "@playwright/test";

/**
 * Browser E2E against the REAL local stack: disposable Keycloak fixture,
 * the production Ants API binary, and the built Next.js console. The
 * orchestration lives in scripts/test-web-e2e.sh so CI and local runs
 * share one truthful environment; this file only describes tests.
 */
const PORT = Number(process.env.ANTS_WEB_PORT ?? 3100);

export default defineConfig({
  testDir: "./e2e",
  timeout: 120_000,
  expect: { timeout: 15_000 },
  fullyParallel: true,
  retries: 0,
  reporter: [["list"]],
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    headless: true,
    screenshot: "only-on-failure",
    trace: "retain-on-failure",
    locale: "en-US",
  },
  outputDir: "test-results/",
});
