import { useQueryClient } from "@tanstack/react-query";
import { Link, NavLink, Navigate, Route, Routes } from "react-router-dom";
import { ApiError, errorMessage } from "./api/client";
import { keys, useLogout, useMe } from "./api/hooks";
import { AuditPage } from "./pages/AuditPage";
import { Dashboard } from "./pages/Dashboard";
import { Login } from "./pages/Login";
import { OrgPage } from "./pages/OrgPage";
import { RepoPage } from "./pages/RepoPage";

export function App() {
  const me = useMe();
  const qc = useQueryClient();
  const logout = useLogout();

  if (me.isLoading) return <p className="empty" role="status">Loading…</p>;
  if (me.error instanceof ApiError && me.error.status === 401) {
    return <Login onLoggedIn={() => void qc.invalidateQueries({ queryKey: keys.me })} />;
  }
  if (me.isError) {
    return (
      <main>
        <p className="notice error" role="alert">Could not reach the server: {errorMessage(me.error)}</p>
        <button className="btn" onClick={() => void me.refetch()}>Retry</button>
      </main>
    );
  }
  const user = me.data!;
  return (
    <>
      <a className="skip" href="#main">Skip to content</a>
      <header className="header">
        <div className="header-inner">
          <Link className="brand" to="/"><span aria-hidden="true">❄</span> frozen-throne</Link>
          <nav className="nav" aria-label="Main">
            <NavLink to="/" end>Repositories</NavLink>
            <NavLink to="/audit">Audit log</NavLink>
          </nav>
          <div className="user">
            <span>{user.name || user.login}</span>
            <button className="btn sm" disabled={logout.isPending} onClick={() => logout.mutate()}>Sign out</button>
          </div>
        </div>
      </header>
      <main id="main" tabIndex={-1}>
        <Routes>
          <Route path="/" element={<Dashboard />} />
          <Route path="/repos/:owner/:repo" element={<RepoPage />} />
          <Route path="/orgs/:org" element={<OrgPage />} />
          <Route path="/audit" element={<AuditPage />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </>
  );
}
