"use client";

import { useRef, useState } from "react";

/**
 * Message composer. Enter sends (Shift+Enter breaks the line). Draft text
 * survives a failed send: the field clears only after the server accepted
 * the message, so nothing the user wrote is ever dropped silently.
 */
export function Composer({
  disabled,
  onSubmit,
}: {
  disabled: boolean;
  onSubmit: (content: string) => Promise<unknown>;
}) {
  const [value, setValue] = useState("");
  const [failed, setFailed] = useState(false);
  const areaRef = useRef<HTMLTextAreaElement>(null);

  async function send() {
    const content = value.trim();
    if (!content || disabled) return;
    try {
      await onSubmit(content);
      setValue("");
      setFailed(false);
      areaRef.current?.focus();
    } catch {
      // The draft stays in the field with an explicit failure note.
      setFailed(true);
    }
  }

  return (
    <form
      className="composer"
      data-testid="composer-form"
      onSubmit={(event) => {
        event.preventDefault();
        void send();
      }}
    >
      <div style={{ flex: "1 1 0", minWidth: 0 }}>
        <label className="label" htmlFor="composer-input" style={{ position: "absolute", left: -9999 }}>
          Add a message
        </label>
        <textarea
          id="composer-input"
          ref={areaRef}
          data-testid="composer"
          className="textarea"
          value={value}
          placeholder="Describe the outcome, add context, or steer…"
          onChange={(event) => setValue(event.target.value)}
          onKeyDown={(event) => {
            if (event.key === "Enter" && !event.shiftKey) {
              event.preventDefault();
              void send();
            }
          }}
        />
        {failed ? (
          <div role="alert" className="banner banner-attention" style={{ marginTop: 8 }}>
            <span>The message was not delivered. It is still here — press Send message to retry.</span>
          </div>
        ) : null}
      </div>
      <button type="submit" className="btn btn-primary" data-testid="send-message" disabled={disabled || !value.trim()}>
        Send message
      </button>
    </form>
  );
}
