import { redirect } from "next/navigation";

import { LoginForm } from "@/app/login/login-form";
import { safeRedirectPath } from "@/lib/origin";
import { readRawSession } from "@/lib/session";

export const dynamic = "force-dynamic";

const ERROR_COPY: Record<string, string> = {
  login_expired:
    "The sign-in attempt expired before it finished. Start again to continue.",
  state_mismatch:
    "The sign-in response did not match this browser's request. For your safety nothing was signed in; start again.",
  code_exchange_rejected:
    "The identity provider refused the sign-in exchange. Start again; if it repeats, the provider may be temporarily unavailable.",
  missing_tenant_claim:
    "Your account is not linked to an Ants workspace. Ask an administrator to provision your identity.",
  tenant_bootstrap_failed:
    "Your workspace could not be prepared. Try again shortly; if it persists, contact an operator.",
  provider_unavailable:
    "The identity provider could not be reached. Try again in a moment.",
};

export default async function LoginPage({
  searchParams,
}: {
  searchParams: Promise<{ error?: string; next?: string }>;
}) {
  const params = await searchParams;
  const next = safeRedirectPath(params.next) ?? "/threads";

  const existing = await readRawSession().then((s) => s !== undefined).catch(() => false);
  if (existing) {
    redirect(next);
  }

  const errorKey = params.error && ERROR_COPY[params.error] ? params.error : undefined;

  return (
    <main id="main" className="main" style={{ maxWidth: 420, margin: "0 auto", paddingTop: "14vh" }}>
      <div className="card" style={{ padding: 28 }}>
        <h1 style={{ marginBottom: 4 }}>Sign in to Ants</h1>
        <p style={{ color: "var(--ink-2)", marginTop: 0 }}>
          One button, your real identity provider. Credentials stay with the
          provider; this console never sees them.
        </p>
        <LoginForm next={next} errorCopy={errorKey ? ERROR_COPY[errorKey] : undefined} />
      </div>
    </main>
  );
}
