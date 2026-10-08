import { useEffect, useState, type ReactNode } from "react";
import { api, type Repo, type Workflow, type WorkflowRun } from "./api/client";
import { Canvas } from "./canvas/Canvas";

type Route =
  | { kind: "home" }
  | { kind: "workflows"; repo: string }
  | { kind: "workflow"; repo: string; workflowId: string }
  | { kind: "run"; repo: string; runId: string }
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
  // /runs/:repo alone is not a collection page
  if (parts[0] === "runs" && parts.length === 3) {
    return {
      kind: "run",
      repo: decodeURIComponent(parts[1]),
      runId: decodeURIComponent(parts[2]),
    };
  }
  return { kind: "notfound" };
}

function navigate(href: string) {
  window.history.pushState({}, "", href);
  window.dispatchEvent(new PopStateEvent("popstate"));
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
        navigate(href);
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

type HomeWorkflow = Workflow & { repo: string };
type HomeRun = WorkflowRun & { repo: string };

const HOME_RUN_CAP = 50;

function Home() {
  const [repos, setRepos] = useState<Repo[]>([]);
  const [workflows, setWorkflows] = useState<HomeWorkflow[]>([]);
  const [runs, setRuns] = useState<HomeRun[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    void (async () => {
      const { data, error: err } = await api.GET("/api/repos");
      if (err) {
        setError(typeof err === "string" ? err : "failed to list Repos");
        return;
      }
      const list = data ?? [];
      setRepos(list);
      // ponytail: client fan-out; aggregate API if repo count hurts latency
      const wfChunks = await Promise.all(
        list.map(async (r) => {
          const res = await api.GET("/api/workflows/{repo}", {
            params: { path: { repo: r.name } },
          });
          return (res.data ?? []).map((w) => ({ ...w, repo: r.name }));
        }),
      );
      const runChunks = await Promise.all(
        list.map(async (r) => {
          const res = await api.GET("/api/runs/{repo}", {
            params: { path: { repo: r.name } },
          });
          return (res.data ?? []).map((run) => ({ ...run, repo: r.name }));
        }),
      );
      const allWf = wfChunks.flat().sort((a, b) => {
        const byRepo = a.repo.localeCompare(b.repo);
        if (byRepo !== 0) return byRepo;
        return (a.id ?? "").localeCompare(b.id ?? "");
      });
      const allRuns = runChunks
        .flat()
        .sort((a, b) => (b.id ?? "").localeCompare(a.id ?? ""))
        .slice(0, HOME_RUN_CAP);
      setWorkflows(allWf);
      setRuns(allRuns);
    })();
  }, []);
  return (
    <Shell title="Home">
      {error && <p className="debugger-error">{error}</p>}
      {repos.length === 0 ? (
        <p className="muted">No Repos registered. Use subspace repo add.</p>
      ) : (
        <>
          <h2 style={{ fontSize: "1rem" }}>Repos</h2>
          <ul className="history-list">
            {repos.map((r) => (
              <li key={r.name}>
                <Link href={`/workflows/${encodeURIComponent(r.name)}`}>{r.name}</Link>
                <span> · {r.absolutePath}</span>
              </li>
            ))}
          </ul>
          <h2 style={{ marginTop: "1.5rem", fontSize: "1rem" }}>Workflows</h2>
          {workflows.length === 0 ? (
            <p className="muted">No Workflows yet.</p>
          ) : (
            <ul className="history-list">
              {workflows.map((w) => (
                <li key={`${w.repo}/${w.id}`}>
                  <Link
                    href={`/workflows/${encodeURIComponent(w.repo)}/${encodeURIComponent(w.id)}`}
                  >
                    {w.name || w.id}
                  </Link>
                  <span>
                    {" "}
                    · {w.repo} · {w.id}
                  </span>
                </li>
              ))}
            </ul>
          )}
          <h2 style={{ marginTop: "1.5rem", fontSize: "1rem" }}>Recent WorkflowRuns</h2>
          {runs.length === 0 ? (
            <p className="muted">No WorkflowRuns yet.</p>
          ) : (
            <ul className="history-list">
              {runs.map((r) => (
                <li key={`${r.repo}/${r.id}`}>
                  <Link href={`/runs/${encodeURIComponent(r.repo)}/${encodeURIComponent(r.id)}`}>
                    {r.id}
                  </Link>
                  <span>
                    {" "}
                    · {r.repo} · {r.workflowId} · {r.status}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Shell>
  );
}

function WorkflowList({ repo }: { repo: string }) {
  const [list, setList] = useState<Workflow[]>([]);
  const [runs, setRuns] = useState<WorkflowRun[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    void (async () => {
      const wfRes = await api.GET("/api/workflows/{repo}", {
        params: { path: { repo } },
      });
      if (wfRes.response?.status === 404) {
        setError(`Unknown Repo: ${repo}`);
        return;
      }
      if (wfRes.error || !wfRes.data) {
        setError(typeof wfRes.error === "string" ? wfRes.error : "failed to list Workflows");
        return;
      }
      setList(wfRes.data);
      const runRes = await api.GET("/api/runs/{repo}", {
        params: { path: { repo } },
      });
      if (!runRes.error && runRes.data) {
        setRuns(runRes.data.slice(0, 50));
      }
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
      {!error && (
        <>
          <h2 style={{ marginTop: "1.5rem", fontSize: "1rem" }}>Recent WorkflowRuns</h2>
          {runs.length === 0 ? (
            <p className="muted">No WorkflowRuns yet.</p>
          ) : (
            <ul className="history-list">
              {runs.map((r) => (
                <li key={r.id}>
                  <Link href={`/runs/${encodeURIComponent(repo)}/${encodeURIComponent(r.id)}`}>
                    {r.id}
                  </Link>
                  <span>
                    {" "}
                    · {r.workflowId} · {r.status}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </>
      )}
    </Shell>
  );
}

function WorkflowCanvas({ repo, workflowId }: { repo: string; workflowId: string }) {
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [error, setError] = useState("");
  const [starting, setStarting] = useState(false);
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

  async function startRun() {
    setStarting(true);
    setError("");
    const { data, error: err, response } = await api.POST("/api/runs/{repo}", {
      params: { path: { repo } },
      body: { workflowId },
    });
    setStarting(false);
    if (response?.status === 404) {
      setError(`Unknown Repo: ${repo}`);
      return;
    }
    if (err || !data) {
      setError(typeof err === "string" ? err : "failed to start WorkflowRun");
      return;
    }
    navigate(`/runs/${encodeURIComponent(repo)}/${encodeURIComponent(data.id)}`);
  }

  return (
    <Shell title={workflow?.name ?? workflowId}>
      <p>
        <Link href={`/workflows/${encodeURIComponent(repo)}`}>← {repo}</Link>
      </p>
      {error && <p className="debugger-error">{error}</p>}
      {workflow && (
        <div className="debugger-canvas" style={{ minHeight: "60vh" }}>
          <p style={{ marginBottom: "0.75rem" }}>
            <button type="button" onClick={() => void startRun()} disabled={starting}>
              {starting ? "Starting…" : "Start WorkflowRun"}
            </button>
          </p>
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

function RunStatus({ repo, runId }: { repo: string; runId: string }) {
  const [run, setRun] = useState<WorkflowRun | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    let cancelled = false;
    const tick = async () => {
      const { data, error: err, response } = await api.GET("/api/runs/{repo}/{runId}", {
        params: { path: { repo, runId } },
      });
      if (cancelled) return;
      if (response?.status === 404) {
        setError(`Unknown Repo or WorkflowRun: ${repo}/${runId}`);
        return;
      }
      if (err || !data) {
        setError(typeof err === "string" ? err : "failed to load WorkflowRun");
        return;
      }
      setRun(data);
    };
    void tick();
    const id = window.setInterval(() => void tick(), 500);
    return () => {
      cancelled = true;
      window.clearInterval(id);
    };
  }, [repo, runId]);

  return (
    <Shell title={run ? `${run.status} · ${runId}` : runId}>
      <p>
        <Link href={`/workflows/${encodeURIComponent(repo)}`}>← {repo}</Link>
        {run && (
          <>
            {" · "}
            <Link
              href={`/workflows/${encodeURIComponent(repo)}/${encodeURIComponent(run.workflowId)}`}
            >
              {run.workflowId}
            </Link>
          </>
        )}
      </p>
      {error && <p className="debugger-error">{error}</p>}
      {run && (
        <div>
          <p>
            WorkflowRun <code>{run.id}</code> · status <strong>{run.status}</strong>
          </p>
          <p className="muted">Debugger controls land in a later ticket.</p>
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
    case "run":
      return <RunStatus repo={route.repo} runId={route.runId} />;
    default:
      return (
        <Shell title="Not found">
          <p className="debugger-error">Unknown path.</p>
          <Link href="/">Home</Link>
        </Shell>
      );
  }
}
