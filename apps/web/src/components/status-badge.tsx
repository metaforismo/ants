import { KIND_COLOR, type StatusKind } from "@/lib/status";

/** Shape-coded status icon: pulse ring (live), diamond (attention), check
 * (done), cross (failed), hollow dot (idle). One stroke weight, in-world. */
function StatusGlyph({ kind }: { kind: StatusKind }) {
  const stroke = KIND_COLOR[kind];
  switch (kind) {
    case "live":
      return (
        <svg className="status-dot" data-status="running" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <circle cx="5" cy="5" r="3.4" fill="none" stroke={stroke} strokeWidth="1.5" />
        </svg>
      );
    case "attention":
      return (
        <svg className="status-dot" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <rect x="2.1" y="2.1" width="5.8" height="5.8" transform="rotate(45 5 5)" fill={stroke} />
        </svg>
      );
    case "done":
      return (
        <svg className="status-dot" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <path d="M1.8 5.4 4 7.6 8.2 2.6" fill="none" stroke={stroke} strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
        </svg>
      );
    case "failed":
      return (
        <svg className="status-dot" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <path d="M2.4 2.4 7.6 7.6 M7.6 2.4 2.4 7.6" fill="none" stroke={stroke} strokeWidth="1.5" strokeLinecap="round" />
        </svg>
      );
    case "idle":
      return (
        <svg className="status-dot" width="10" height="10" viewBox="0 0 10 10" aria-hidden="true">
          <circle cx="5" cy="5" r="3.4" fill="none" stroke={stroke} strokeWidth="1.5" />
        </svg>
      );
  }
}

export function StatusBadge({
  label,
  kind,
}: {
  label: string;
  kind: StatusKind;
}) {
  return (
    <span className="status" style={{ color: KIND_COLOR[kind] }}>
      <StatusGlyph kind={kind} />
      <span style={{ color: "var(--ink-2)" }}>{label}</span>
    </span>
  );
}
