import { useState } from "react";
import { useLocation } from "react-router-dom";
import { errorMessage } from "../api/client";
import { isMock, loginUrl, nav } from "../config";

export function Login({ onLoggedIn, expired }: { onLoggedIn?: () => void; expired?: boolean }) {
  const loc = useLocation();
  const [err, setErr] = useState("");
  const mock = isMock();
  const mockLogin = async () => {
    try {
      const r = await fetch("/api/v1/auth/mock-login", { method: "POST", headers: { "X-Requested-With": "frozen-throne" } });
      if (!r.ok) throw new Error(`Mock login failed (${r.status})`);
      onLoggedIn?.();
    } catch (e) { setErr(errorMessage(e)); }
  };
  // Real mode: full-page navigation so the server can set cookies and redirect through GitHub.
  const signIn = () => nav.to(loginUrl(loc.pathname + loc.search));
  return (
    <main className="login">
      <div className="flake" aria-hidden="true">❄</div>
      <h1>frozen-throne</h1>
      {expired && <p className="notice error" role="alert">Your session expired, sign in again</p>}
      <p className="muted">Merge freezes for GitHub. Sign in to see and control your repositories.</p>
      {mock ? (
        <button className="btn primary big" onClick={mockLogin}>Sign in (mock API)</button>
      ) : (
        <button className="btn primary big" onClick={signIn}>Sign in with GitHub</button>
      )}
      {err && <p className="error" role="alert">{err}</p>}
    </main>
  );
}
