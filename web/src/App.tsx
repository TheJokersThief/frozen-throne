import type { components } from "./api/schema";

type RepoSummary = components["schemas"]["RepoSummary"];

// Skeleton only: the dashboard is built by the web task against a mocked API.
export function App() {
  const repos: RepoSummary[] = [];
  return (
    <main>
      <h1>frozen-throne</h1>
      <p>{repos.length} repos</p>
    </main>
  );
}
