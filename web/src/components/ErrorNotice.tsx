import { Link } from "react-router-dom";
import { errorMessage } from "../api/client";

/** Load failure (5xx / network) with a way to try again. */
export function ErrorNotice({ error, what, onRetry }: { error: unknown; what: string; onRetry: () => void }) {
  return (
    <div className="notice error" role="alert">
      <p>Could not load {what}: {errorMessage(error)}</p>
      <button type="button" className="btn sm" onClick={onRetry}>Retry</button>
    </div>
  );
}

/** The API answers 404 for anything the user cannot see, so say both. */
export function NotFound({ title, what }: { title: string; what: string }) {
  return (
    <section className="empty" role="alert">
      <h1>{title}</h1>
      <p>This {what} does not exist or you do not have access to it.</p>
      <p><Link to="/">Back to the dashboard</Link></p>
    </section>
  );
}
