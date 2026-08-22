// Smoke test for the generated contract: the generated schema must expose
// every entity the platform promises to clients. If generation silently
// produces an empty or partial file, this fails.
import { test } from "node:test";
import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { dirname, join } from "node:path";

const here = dirname(fileURLToPath(import.meta.url));
const generated = readFileSync(join(here, "../src/schema.d.ts"), "utf8");

for (const entity of [
  "Tenant",
  "Project",
  "Thread",
  "Message",
  "Run",
  "Task",
  "RunReport",
  "Event",
  "Problem",
  "SpecContent",
  "Finding",
  "Evidence",
]) {
  test(`generated contract exposes ${entity}`, () => {
    assert.ok(
      new RegExp(`${entity}:`).test(generated),
      `${entity} missing from generated schema`,
    );
  });
}

for (const path of [
  "/v1/tenants",
  "/v1/projects",
  "/v1/threads/{id}",
  "/v1/threads/{id}/runs",
  "/v1/runs/{id}/events",
  "/v1/runs/{id}/report",
  "/v1/artifacts/{id}",
]) {
  test(`generated contract covers ${path}`, () => {
    assert.ok(generated.includes(path), `path ${path} missing from generated schema`);
  });
}
