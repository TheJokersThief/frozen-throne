import { useState } from "react";
import { errorMessage } from "../api/client";

export const IS_MOCK = import.meta.env.VITE_MOCK === "1";

export function Login({ onLoggedIn }: { onLoggedIn?: () => void }) {
  const [err, setErr] = useState("");
  const redirect = encodeURIComponent(location.pathname + location.search);
  const mockLogin = async () => {
    try {
      const r = await fetch("/api/v1/auth/mock-login", { method: "POST", headers: { "X-Requested-With": "frozen-throne" } });
      if (!r.ok) throw new Error(`Mock login failed (${r.status})`);
      onLoggedIn?.();
    } catch (e) { setErr(errorMessage(e)); }
  };
  return (
    <main className="login">
      <div className="flake" aria-hidden="true">❄</div>
      <h1>frozen-throne</h1>
      <p className="muted">Merge freezes for GitHub. Sign in to see and control your repositories.</p>
      {IS_MOCK ? (
        <button className="btn primary big" onClick={mockLogin}>Sign in (mock API)</button>
      ) : (
        <a className="btn primary big" style={{ display: "inline-block", textDecoration: "none" }} href={`/api/v1/auth/login?redirect=${redirect}`}>
          Sign in with GitHub
        </a>
      )}
      {err && <p className="error" role="alert">{err}</p>}
    </main>
  );
}
