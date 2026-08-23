"use client";

import { useEffect, useRef, useState } from "react";

import { RelativeTime } from "@/components/relative-time";
import { api, errorCode } from "@/lib/client-api";
import type { Event } from "@ants/contracts";

const POLL_MS = 1500;
const MAX_RENDERED = 50;

/**
 * Event trail with honest cursor resume: the poll always continues from
 * the last observed `seq`, so reconnects and reloads neither replay nor
 * skip durable history. Newest entries enter with the left-to-right trail.
 */
export function EventTrail({ runId, active }: { runId: string; active: boolean }) {
  const [events, setEvents] = useState<Event[]>([]);
  const [lastSeq, setLastSeq] = useState(0);
  const [failed, setFailed] = useState<"transient" | "expired" | undefined>(undefined);
  const cursor = useRef(0);

  useEffect(() => {
    if (!runId) return;
    let cancelled = false;

    async function tick() {
      try {
        const page = await api.listRunEvents(runId, cursor.current);
        if (cancelled) return;
        setFailed(undefined);
        const fresh = page.events ?? [];
        if (fresh.length > 0) {
          const lastSeq = fresh[fresh.length - 1]?.seq ?? cursor.current;
          if (lastSeq > cursor.current) {
            cursor.current = lastSeq;
            setLastSeq(lastSeq);
          }
          setEvents((prev) => {
            const seen = new Set(prev.map((e) => e.id));
            return [...prev, ...fresh.filter((e) => !seen.has(e.id))].slice(-MAX_RENDERED);
          });
        }
      } catch (err) {
        if (cancelled) return;
        const code = errorCode(err);
        setFailed(code === "session_expired" ? "expired" : "transient");
      }
    }

    // Resume immediately from the stored cursor, then keep the cadence
    // while the run is live.
    void tick();
    if (!active) return;
    const timer = window.setInterval(() => void tick(), POLL_MS);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [runId, active]);

  return (
    <div>
      <h3 style={{ fontSize: 13, marginBottom: 6 }}>
        Events{" "}
        <span className="mono" style={{ color: "var(--ink-3)" }}>
          from seq {lastSeq}
        </span>
      </h3>
      {failed === "expired" ? (
        <div role="alert" className="banner banner-attention" data-testid="events-expired">
          <span>The event stream paused: sign in again to resume.</span>
        </div>
      ) : failed === "transient" ? (
        <div role="status" className="banner banner-offline" data-testid="events-reconnecting">
          <span>Reconnecting… the trail resumes from its cursor.</span>
        </div>
      ) : null}
      {events.length === 0 ? (
        <p style={{ color: "var(--ink-3)", margin: "4px 0 0" }}>No events yet.</p>
      ) : (
        <ol
          className="stagger"
          data-testid="event-trail"
          aria-live="polite"
          style={{ listStyle: "none", margin: "8px 0 0", padding: 0, display: "grid", gap: 4 }}
        >
          {events.map((event) => (
            <li
              key={event.id}
              className="trail-in mono"
              style={{
                display: "grid",
                gridTemplateColumns: "auto auto minmax(0,1fr) auto",
                gap: 10,
                fontSize: 12,
                borderBottom: "1px solid var(--surface-sunken)",
                padding: "3px 2px",
              }}
            >
              <span style={{ color: "var(--ink-3)" }}>#{event.seq}</span>
              <span>{event.type}</span>
              <span style={{ color: "var(--ink-2)", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                {summarize(event)}
              </span>
              <RelativeTime at={event.occurred_at} />
            </li>
          ))}
        </ol>
      )}
    </div>
  );
}

/** One-line human summary derived from the versioned event payload. */
function summarize(event: Event): string {
  const data = event.data as Record<string, unknown>;
  const parts: string[] = [];
  for (const field of ["task_id", "from_status", "to_status", "reason", "outcome"]) {
    if (typeof data[field] === "string") parts.push(`${field}=${data[field]}`);
  }
  return parts.join(" ");
}
