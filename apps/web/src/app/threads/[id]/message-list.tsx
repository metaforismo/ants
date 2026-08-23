"use client";

import { RelativeTime } from "@/components/relative-time";

export type MessageView = {
  id: string;
  seq: number;
  role: "user" | "agent" | "system";
  content: string;
  createdAt: string;
};

export function MessageList({
  messages,
  loading,
  error,
  onRetry,
}: {
  messages: MessageView[];
  loading: boolean;
  error?: "error" | "session_expired";
  onRetry: () => void;
}) {
  if (loading) {
    return (
      <div aria-hidden="true" data-testid="messages-loading">
        <div className="skeleton-row" />
        <div className="skeleton-row" />
      </div>
    );
  }
  if (error === "session_expired") {
    return <SessionExpiredInline />;
  }
  if (error) {
    return (
      <div role="alert" className="state-panel card" data-testid="messages-error">
        <p className="state-title">Could not load the conversation</p>
        <button type="button" className="btn" onClick={onRetry}>
          Retry
        </button>
      </div>
    );
  }
  if (messages.length === 0) {
    return (
      <div className="state-panel card" data-testid="messages-empty">
        <p className="state-title">No messages yet</p>
        <p>Start below: describe the outcome in one or two sentences.</p>
      </div>
    );
  }
  return (
    <ol className="stagger" data-testid="message-list" style={{ listStyle: "none", margin: 0, padding: 0, display: "grid", gap: 8 }}>
      {messages.map((message) => (
        <li key={message.id} className="message trail-in">
          <div className="message-role">
            {message.role} · <span className="mono">#{message.seq}</span> ·{" "}
            <RelativeTime at={message.createdAt} />
          </div>
          <div className="message-content">{message.content}</div>
        </li>
      ))}
    </ol>
  );
}

function SessionExpiredInline() {
  return (
    <div className="state-panel card" data-testid="session-expired">
      <p className="state-title">Your session expired</p>
      <p>Sign in again to continue where you left off.</p>
      <a className="btn btn-primary" href="/api/auth/login?next=%2Fthreads" style={{ textDecoration: "none" }}>
        Sign in again
      </a>
    </div>
  );
}
