import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  API_BASE,
  api,
  type Repo,
  type TimelineEvent,
  type Workflow,
  type WorkflowRun,
} from "./api/client";
import { Canvas, type StepStatus } from "./canvas/Canvas";

function wsURL(path: string) {
  return `${API_BASE.replace(/^http/, "ws")}${path}`;
}

function deriveStepStatus(run: WorkflowRun | null, events: TimelineEvent[]): Record<string, StepStatus> {
  const out: Record<string, StepStatus> = {};
  for (const sr of run?.stepRuns ?? []) {
    const s = sr.status.toLowerCase();
    if (s === "running" || s === "started") out[sr.stepId] = "running";
    else if (s === "succeeded" || s === "success") out[sr.stepId] = "succeeded";
    else if (s === "failed" || s === "error") out[sr.stepId] = "failed";
    else if (s === "stopped") out[sr.stepId] = "stopped";
    else out[sr.stepId] = "idle";
  }
  for (const ev of events) {
    const sid = ev.stepId;
    if (!sid) continue;
    if (ev.type === "step_started") out[sid] = "running";
    else if (ev.type === "step_succeeded") out[sid] = "succeeded";
    else if (ev.type === "step_failed") out[sid] = "failed";
  }
  if (run?.status === "failed" || run?.status === "stopped") {
    for (const [id, st] of Object.entries(out)) {
      if (st === "running") out[id] = run.status === "stopped" ? "stopped" : "failed";
    }
  }
  return out;
}

function statusTone(status: string | undefined): string {
  if (!status) return "idle";
  const s = status.toLowerCase();
  if (s === "running" || s === "started") return "running";
  if (s === "succeeded" || s === "success") return "succeeded";
  if (s === "failed" || s === "error") return "failed";
  if (s === "stopped") return "stopped";
  if (s === "paused") return "paused";
  return "idle";
}

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

function RunDebugger({ repo, runId }: { repo: string; runId: string }) {
  const [run, setRun] = useState<WorkflowRun | null>(null);
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [events, setEvents] = useState<TimelineEvent[]>([]);
  const [timelineKey, setTimelineKey] = useState(0);
  const [rewindStepId, setRewindStepId] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const stepStatus = useMemo(() => deriveStepStatus(run, events), [run, events]);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
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
      const wf = await api.GET("/api/workflows/{repo}/{workflowId}", {
        params: { path: { repo, workflowId: data.workflowId } },
      });
      if (!cancelled && wf.data) setWorkflow(wf.data);
    })();
    return () => {
      cancelled = true;
    };
  }, [repo, runId]);

  useEffect(() => {
    if (!run?.id) return;
    setEvents([]);
    const path = `/api/runs/${encodeURIComponent(repo)}/${encodeURIComponent(run.id)}/events`;
    const ws = new WebSocket(wsURL(path));
    ws.onmessage = (msg) => {
      const ev = JSON.parse(msg.data) as TimelineEvent;
      setEvents((prev) => [...prev, ev]);
      if (ev.type === "workflow_succeeded" || ev.type === "workflow_failed" || ev.type === "workflow_stopped") {
        const status =
          ev.type === "workflow_succeeded" ? "succeeded" : ev.type === "workflow_stopped" ? "stopped" : "failed";
        setRun((r) => (r ? { ...r, status } : r));
      }
      if (ev.type === "workflow_paused") {
        setRun((r) => (r ? { ...r, status: "paused" } : r));
      }
      if (ev.type === "step_started" && ev.stepId) {
        setRun((r) => (r ? { ...r, cursorStepId: ev.stepId } : r));
      }
    };
    ws.onerror = () => setError("WebSocket error");
    return () => ws.close();
  }, [repo, run?.id, timelineKey]);

  async function control(action: "pause" | "resume" | "stop") {
    setBusy(true);
    setError("");
    try {
      const path =
        action === "pause"
          ? "/api/runs/{repo}/{runId}/pause"
          : action === "resume"
            ? "/api/runs/{repo}/{runId}/resume"
            : "/api/runs/{repo}/{runId}/stop";
      const { data, error: err } = await api.POST(path, {
        params: { path: { repo, runId } },
      });
      if (err || !data) throw new Error(typeof err === "string" ? err : `${action} failed`);
      setRun(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function rewind() {
    if (!rewindStepId.trim()) return;
    setBusy(true);
    setError("");
    try {
      const { data, error: err } = await api.POST("/api/runs/{repo}/{runId}/rewind", {
        params: { path: { repo, runId } },
        body: { stepId: rewindStepId.trim() },
      });
      if (err || !data) throw new Error(typeof err === "string" ? err : "rewind failed");
      setRun(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function refresh() {
    setBusy(true);
    setError("");
    try {
      const { data, error: err } = await api.GET("/api/runs/{repo}/{runId}", {
        params: { path: { repo, runId } },
      });
      if (err || !data) throw new Error(typeof err === "string" ? err : "getRun failed");
      setRun(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  const tone = statusTone(run?.status);

  return (
    <div className="debugger">
      <header className="debugger-header">
        <div className="debugger-header-inner">
          <h1 className="debugger-brand">
            <Link href="/">Subspace</Link>
          </h1>
          <div className="debugger-meta">
            <span className="debugger-status" data-tone={tone}>
              {run?.status ?? "loading"}
            </span>
            <span className="debugger-run-id">{runId}</span>
            <Link href={`/workflows/${encodeURIComponent(repo)}`}>{repo}</Link>
            {run && (
              <Link
                href={`/workflows/${encodeURIComponent(repo)}/${encodeURIComponent(run.workflowId)}`}
              >
                {run.workflowId}
              </Link>
            )}
          </div>
          <div className="toolbar" role="toolbar" aria-label="WorkflowRun controls">
            <button className="btn-pause" disabled={busy || !run} onClick={() => void control("pause")}>
              Pause
            </button>
            <button className="btn-start" disabled={busy || !run} onClick={() => void control("resume")}>
              Resume
            </button>
            <button className="btn-stop" disabled={busy || !run} onClick={() => void control("stop")}>
              Stop
            </button>
            <button
              className="btn-rewind"
              disabled={busy || !run || !rewindStepId.trim()}
              onClick={() => void rewind()}
            >
              Rewind
            </button>
            <button disabled={busy || !run} onClick={() => void refresh()}>
              Refresh
            </button>
            <button
              disabled={!run}
              onClick={() => {
                setEvents([]);
                setTimelineKey((k) => k + 1);
              }}
            >
              Reconnect
            </button>
          </div>
        </div>
      </header>

      <div className="debugger-body">
        {error && <p className="debugger-error">{error}</p>}
        <div className="debugger-split">
          <div className="debugger-canvas">
            {workflow ? (
              <div className="wf-canvas">
                <Canvas
                  steps={workflow.steps}
                  connections={workflow.connections}
                  stepStatus={stepStatus}
                  cursorStepId={run?.cursorStepId}
                  onSelectStep={setRewindStepId}
                />
              </div>
            ) : (
              <div className="canvas-empty">Loading Workflow canvas…</div>
            )}
          </div>
          <aside className="debugger-panel">
            <section className="panel-section">
              <h2>Rewind</h2>
              <label className="field">
                Step ID (click a canvas Step)
                <span className="inline-row">
                  <input value={rewindStepId} onChange={(e) => setRewindStepId(e.target.value)} />
                  <button
                    className="panel-btn danger"
                    disabled={busy || !run || !rewindStepId.trim()}
                    onClick={() => void rewind()}
                  >
                    Rewind
                  </button>
                </span>
              </label>
            </section>
            <section className="panel-section">
              <h2>Timeline</h2>
              {events.length === 0 ? (
                <p className="muted">Timeline events appear when the WebSocket connects.</p>
              ) : (
                <ol className="timeline-list">
                  {events.map((ev, i) => (
                    <li key={i}>
                      <span className="timeline-type">{ev.type}</span>
                      {ev.stepId != null && <span> {ev.stepId}</span>}
                      {typeof ev.iteration === "number" && ev.iteration > 0 && (
                        <span> iter={String(ev.iteration)}</span>
                      )}
                      {ev.data != null && <span> {ev.data}</span>}
                      {ev.processId != null && ev.data == null && <span> {ev.processId}</span>}
                    </li>
                  ))}
                </ol>
              )}
            </section>
          </aside>
        </div>
      </div>
    </div>
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
      return <RunDebugger repo={route.repo} runId={route.runId} />;
    default:
      return (
        <Shell title="Not found">
          <p className="debugger-error">Unknown path.</p>
          <Link href="/">Home</Link>
        </Shell>
      );
  }
}
