import { useEffect, useState, type ReactNode } from "react";
import { api, type Repo, type Workflow } from "./api/client";
import { Canvas } from "./canvas/Canvas";

type Route =
  | { kind: "home" }
  | { kind: "workflows"; repo: string }
  | { kind: "workflow"; repo: string; workflowId: string }
  | { kind: "notfound" };

function parseRoute(pathname: string): Route {
  const parts = pathname.replace(/\/+$/, "").split("/").filter(Boolean);
  if (parts.length === 0) return { kind: "home" };
  if (parts[0] === "workflows" && parts.length === 2) {
    return { kind: "workflows", repo: decodeURIComponent(parts[1]) };
  }
  if (parts[0] === "workflows" && parts.length === 3) {
    return {
      kind: "workflow",
      repo: decodeURIComponent(parts[1]),
      workflowId: decodeURIComponent(parts[2]),
    };
  }
  return { kind: "notfound" };
}

function useRoute(): Route {
  const [route, setRoute] = useState(() => parseRoute(window.location.pathname));
  useEffect(() => {
    const sync = () => setRoute(parseRoute(window.location.pathname));
    window.addEventListener("popstate", sync);
    return () => window.removeEventListener("popstate", sync);
  }, []);
  return route;
}

function Link({ href, children }: { href: string; children: ReactNode }) {
  return (
    <a
      href={href}
      onClick={(e) => {
        if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || e.button !== 0) return;
        e.preventDefault();
        window.history.pushState({}, "", href);
        window.dispatchEvent(new PopStateEvent("popstate"));
      }}
    >
      {children}
    </a>
  );
}

function Shell({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="debugger">
      <header className="debugger-header">
        <div className="debugger-header-inner">
          <h1 className="debugger-brand">
            <Link href="/">Subspace</Link>
          </h1>
          <div className="debugger-meta">
            <span className="debugger-status" data-tone="idle">
              {title}
            </span>
          </div>
        </div>
      </header>
      <div className="debugger-body" style={{ padding: "1rem" }}>
        {children}
      </div>
    </div>
  );
}

function Home() {
  const [repos, setRepos] = useState<Repo[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    void (async () => {
      const { data, error: err } = await api.GET("/api/repos");
      if (err) {
        setError(typeof err === "string" ? err : "failed to list Repos");
        return;
      }
      setRepos(data ?? []);
    })();
  }, []);
  return (
    <Shell title="Repos">
      {error && <p className="debugger-error">{error}</p>}
      {repos.length === 0 ? (
        <p className="muted">No Repos registered. Use subspace repo add.</p>
      ) : (
        <ul className="history-list">
          {repos.map((r) => (
            <li key={r.name}>
              <Link href={`/workflows/${encodeURIComponent(r.name)}`}>{r.name}</Link>
              <span> · {r.absolutePath}</span>
            </li>
          ))}
        </ul>
      )}
    </Shell>
  );
}

function WorkflowList({ repo }: { repo: string }) {
  const [list, setList] = useState<Workflow[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    void (async () => {
      const { data, error: err, response } = await api.GET("/api/workflows/{repo}", {
        params: { path: { repo } },
      });
      if (response?.status === 404) {
        setError(`Unknown Repo: ${repo}`);
        return;
      }
      if (err || !data) {
        setError(typeof err === "string" ? err : "failed to list Workflows");
        return;
      }
      setList(data);
    })();
  }, [repo]);
  return (
    <Shell title={`Workflows · ${repo}`}>
      <p>
        <Link href="/">← Repos</Link>
      </p>
      {error && <p className="debugger-error">{error}</p>}
      {!error && list.length === 0 && <p className="muted">No Workflows in this Repo.</p>}
      <ul className="history-list">
        {list.map((w) => (
          <li key={w.id}>
            <Link href={`/workflows/${encodeURIComponent(repo)}/${encodeURIComponent(w.id)}`}>
              {w.name || w.id}
            </Link>
            <span> · {w.id}</span>
          </li>
        ))}
      </ul>
    </Shell>
  );
}

function WorkflowCanvas({ repo, workflowId }: { repo: string; workflowId: string }) {
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    void (async () => {
      const { data, error: err, response } = await api.GET("/api/workflows/{repo}/{workflowId}", {
        params: { path: { repo, workflowId } },
      });
      if (response?.status === 404) {
        setError(`Unknown Repo or Workflow: ${repo}/${workflowId}`);
        return;
      }
      if (err || !data) {
        setError(typeof err === "string" ? err : "failed to load Workflow");
        return;
      }
      setWorkflow(data);
    })();
  }, [repo, workflowId]);
  return (
    <Shell title={workflow?.name ?? workflowId}>
      <p>
        <Link href={`/workflows/${encodeURIComponent(repo)}`}>← {repo}</Link>
      </p>
      {error && <p className="debugger-error">{error}</p>}
      {workflow && (
        <div className="debugger-canvas" style={{ minHeight: "60vh" }}>
          <div className="wf-canvas">
            <Canvas
              steps={workflow.steps}
              connections={workflow.connections}
              stepStatus={{}}
            />
          </div>
          <p className="muted" style={{ marginTop: "0.75rem" }}>
            Read-only canvas. Edit Workflow YAML on disk.
          </p>
        </div>
      )}
    </Shell>
  );
}

export default function App() {
  const route = useRoute();
  switch (route.kind) {
    case "home":
      return <Home />;
    case "workflows":
      return <WorkflowList repo={route.repo} />;
    case "workflow":
      return <WorkflowCanvas repo={route.repo} workflowId={route.workflowId} />;
    default:
      return (
        <Shell title="Not found">
          <p className="debugger-error">Unknown path.</p>
          <Link href="/">Home</Link>
        </Shell>
      );
  }
}
