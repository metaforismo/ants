"use client";

import { useRef } from "react";

export function LoginForm({
  next,
  errorCopy,
}: {
  next: string;
  errorCopy?: string;
}) {
  const linkRef = useRef<HTMLAnchorElement>(null);
  return (
    <div style={{ display: "grid", gap: 12 }}>
      {errorCopy ? (
        <div role="alert" className="banner banner-attention">
          <span>{errorCopy}</span>
        </div>
      ) : null}
      {/* The BFF login route mints the PKCE transaction server-side and
          redirects to the provider; the browser only ever follows links. */}
      <a
        ref={linkRef}
        className="btn btn-primary"
        data-testid="login-button"
        href={`/api/auth/login?next=${encodeURIComponent(next)}`}
        style={{ justifyContent: "center", textDecoration: "none" }}
      >
        Continue with identity provider
      </a>
    </div>
  );
}
