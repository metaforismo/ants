"use client";

import { useState } from "react";

function relativeLabel(diffSeconds: number): string {
  if (diffSeconds < 45) return "just now";
  if (diffSeconds < 3600) return `${Math.round(diffSeconds / 60)}m ago`;
  if (diffSeconds < 86400) return `${Math.round(diffSeconds / 3600)}h ago`;
  return `${Math.round(diffSeconds / 86400)}d ago`;
}

/**
 * Relative timestamps with the absolute value in the native tooltip. The
 * reference clock is captured once per mount (a render must stay pure);
 * staleness beyond one bucket resolves on the next natural re-render.
 */
export function RelativeTime({
  at,
  now,
}: {
  at: string;
  /** Injectable clock for tests; defaults to mount time. */
  now?: number;
}) {
  const [mountTime] = useState(() => now ?? Date.now());

  const then = new Date(at).getTime();
  const absolute = Number.isFinite(then)
    ? new Date(then).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })
    : at;
  const diffSeconds = Math.round((mountTime - then) / 1000);
  // Server clocks can run slightly ahead of the browser; never show
  // negative durations.
  const label = !Number.isFinite(then) || diffSeconds < -30 ? "just now" : relativeLabel(diffSeconds);

  return (
    <time dateTime={at} title={absolute} className="mono">
      {label}
    </time>
  );
}
