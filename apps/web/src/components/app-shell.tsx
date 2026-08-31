import Link from "next/link";

import { SignOutButton } from "@/components/sign-out-button";

/**
 * Application frame: left rail on desktop, top bar on mobile (pure CSS).
 * Identity comes from the server-side session only — the browser never
 * receives token material, so the rail can show it without exposure.
 */
export function AppShell({
  username,
  tenantSlug,
  children,
}: {
  username: string;
  tenantSlug: string;
  children: React.ReactNode;
}) {
  return (
    <div className="shell">
      <nav className="rail" aria-label="Primary">
        <Link href="/threads" className="rail-brand">
          <svg width="16" height="16" viewBox="0 0 16 16" aria-hidden="true">
            <ellipse cx="8" cy="10.5" rx="4.6" ry="3.6" fill="none" stroke="var(--accent)" strokeWidth="1.5" />
            <circle cx="8" cy="4" r="2.2" fill="none" stroke="var(--accent)" strokeWidth="1.5" />
          </svg>
          Ants
        </Link>
        <div className="rail-nav">
          <Link href="/threads" className="rail-nav-link">
            Threads
          </Link>
        </div>
        <div className="rail-identity">
          <span className="rail-tenant-label">tenant</span>
          <span className="rail-tenant mono" title={tenantSlug}>
            {tenantSlug}
          </span>
          <span className="rail-user" title={username}>
            {username}
          </span>
          <span className="rail-signout">
            <SignOutButton />
          </span>
        </div>
      </nav>
      <main id="main" className="main">
        {children}
      </main>
    </div>
  );
}
