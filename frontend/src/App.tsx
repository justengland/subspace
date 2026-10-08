import { useEffect, useMemo, useState } from "react";
import { API_BASE, api, type StartRequest, type TimelineEvent, type Workflow, type WorkflowRun } from "./api/client";
import { Canvas, type StepStatus } from "./canvas/Canvas";

/** Parse "step.input=value" lines into InputOverrides. */
function parseInputOverrides(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i <= 0) continue;
    out[t.slice(0, i)] = t.slice(i + 1);
  }
  return out;
}

/** Parse "processId=arg1,arg2" lines into ArgumentOverrides. */
function parseArgOverrides(text: string): Record<string, string[]> {
  const out: Record<string, string[]> = {};
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (!t) continue;
    const i = t.indexOf("=");
    if (i <= 0) continue;
    const id = t.slice(0, i);
    const args = t.slice(i + 1);
    out[id] = args === "" ? [] : args.split(",");
  }
  return out;
}

function wsURL(path: string) {
  const base = API_BASE.replace(/^http/, "ws");
  return `${base}${path}`;
}

/** Map Engine/API status strings to UI tone (header chip + Step canvas). */
function statusTone(status: string | undefined): StepStatus | "paused" {
  if (!status) return "idle";
  const s = status.toLowerCase();
  if (s === "running" || s === "started") return "running";
  if (s === "succeeded" || s === "success") return "succeeded";
  if (s === "failed" || s === "error") return "failed";
  if (s === "stopped") return "stopped";
  if (s === "paused") return "paused";
  return "idle";
}

function asStepStatus(tone: StepStatus | "paused"): StepStatus {
  return tone === "paused" ? "idle" : tone;
}

/** Derive Step status from timeline + WorkflowRun.stepRuns (UI never writes YAML). */
function deriveStepStatus(run: WorkflowRun | null, events: TimelineEvent[]): Record<string, StepStatus> {
  const out: Record<string, StepStatus> = {};
  for (const sr of run?.stepRuns ?? []) {
    out[sr.stepId] = asStepStatus(statusTone(sr.status));
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

export default function App() {
  const [workflowId, setWorkflowId] = useState("pipe");
  const [projectPath, setProjectPath] = useState("");
  const [inputOverrideText, setInputOverrideText] = useState("");
  const [argOverrideText, setArgOverrideText] = useState("");
  const [run, setRun] = useState<WorkflowRun | null>(null);
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [events, setEvents] = useState<TimelineEvent[]>([]);
  const [timelineKey, setTimelineKey] = useState(0);
  const [rewindStepId, setRewindStepId] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const [history, setHistory] = useState<WorkflowRun[]>([]);

  const stepStatus = useMemo(() => deriveStepStatus(run, events), [run, events]);

  useEffect(() => {
    if (!run?.id) return;
    const ws = new WebSocket(wsURL(`/api/runs/${run.id}/events`));
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
  }, [run?.id, timelineKey]);

  async function loadWorkflow(id = workflowId) {
    setBusy(true);
    setError("");
    try {
      const { data, error: err } = await api.GET("/api/workflows/{id}", {
        params: { path: { id } },
      });
      if (err || !data) throw new Error(typeof err === "string" ? err : "getWorkflow failed");
      setWorkflow(data);
      if (!projectPath && data.defaultProject) setProjectPath(data.defaultProject);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function start() {
    setBusy(true);
    setError("");
    setEvents([]);
    try {
      if (!workflow || workflow.id !== workflowId) {
        const { data: wf, error: werr } = await api.GET("/api/workflows/{id}", {
          params: { path: { id: workflowId } },
        });
        if (werr || !wf) throw new Error(typeof werr === "string" ? werr : "getWorkflow failed");
        setWorkflow(wf);
        if (!projectPath && wf.defaultProject) setProjectPath(wf.defaultProject);
      }
      const body: StartRequest = { workflowId };
      if (projectPath) body.projectPath = projectPath;
      const inputs = parseInputOverrides(inputOverrideText);
      if (Object.keys(inputs).length) body.inputOverrides = inputs;
      const args = parseArgOverrides(argOverrideText);
      if (Object.keys(args).length) body.argumentOverrides = args;
      const { data, error: err } = await api.POST("/api/runs", { body });
      if (err || !data) throw new Error(typeof err === "string" ? err : "startRun failed");
      setRun(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function refresh() {
    if (!run) return;
    setBusy(true);
    setError("");
    try {
      const { data, error: err } = await api.GET("/api/runs/{id}", {
        params: { path: { id: run.id } },
      });
      if (err || !data) throw new Error(typeof err === "string" ? err : "getRun failed");
      setRun(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  function reconnect() {
    if (!run) return;
    setEvents([]);
    setTimelineKey((k) => k + 1);
  }

  async function loadHistory() {
    setBusy(true);
    setError("");
    try {
      const { data, error: err } = await api.GET("/api/runs", {
        params: { query: projectPath ? { projectPath } : {} },
      });
      if (err || !data) throw new Error(typeof err === "string" ? err : "listRuns failed");
      setHistory(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  async function reopen(past: WorkflowRun) {
    setEvents([]);
    setRun(past);
    setTimelineKey((k) => k + 1);
    if (past.workflowId && past.workflowId !== workflow?.id) {
      setWorkflowId(past.workflowId);
      await loadWorkflow(past.workflowId);
    }
  }

  async function control(action: "pause" | "resume" | "stop") {
    if (!run) return;
    setBusy(true);
    setError("");
    try {
      const path =
        action === "pause"
          ? "/api/runs/{id}/pause"
          : action === "resume"
            ? "/api/runs/{id}/resume"
            : "/api/runs/{id}/stop";
      const { data, error: err } = await api.POST(path, {
        params: { path: { id: run.id } },
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
    if (!run || !rewindStepId.trim()) return;
    setBusy(true);
    setError("");
    try {
      const { data, error: err } = await api.POST("/api/runs/{id}/rewind", {
        params: { path: { id: run.id } },
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

  const tone = statusTone(run?.status);

  return (
    <div className="debugger">
      <header className="debugger-header">
        <div className="debugger-header-inner">
          <h1 className="debugger-brand">Subspace</h1>
          <div className="debugger-meta">
            <span className="debugger-status" data-tone={tone}>
              {run?.status ?? (workflow ? "ready" : "idle")}
            </span>
            {run && <span className="debugger-run-id">{run.id}</span>}
            <label className="header-field">
              Workflow
              <input value={workflowId} onChange={(e) => setWorkflowId(e.target.value)} />
            </label>
          </div>
          <div className="toolbar" role="toolbar" aria-label="WorkflowRun controls">
            <button className="btn-accent" disabled={busy} onClick={() => loadWorkflow()}>
              Load canvas
            </button>
            <button className="btn-start" disabled={busy} onClick={start}>
              Start
            </button>
            <button className="btn-pause" disabled={busy || !run} onClick={() => control("pause")}>
              Pause
            </button>
            <button className="btn-start" disabled={busy || !run} onClick={() => control("resume")}>
              Resume
            </button>
            <button className="btn-stop" disabled={busy || !run} onClick={() => control("stop")}>
              Stop
            </button>
            <button
              className="btn-rewind"
              disabled={busy || !run || !rewindStepId.trim()}
              onClick={rewind}
            >
              Rewind
            </button>
            <button disabled={busy || !run} onClick={refresh}>
              Refresh
            </button>
            <button disabled={!run} onClick={reconnect}>
              Reconnect timeline
            </button>
            <button disabled={busy} onClick={loadHistory}>
              List runs
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
              <div className="canvas-empty">
                Load a Workflow to show the canvas. Definition YAML stays read-only.
              </div>
            )}
          </div>

          <aside className="debugger-panel">
            <section className="panel-section">
              <h2>Overrides</h2>
              <label className="field">
                Project path
                <input
                  value={projectPath}
                  onChange={(e) => setProjectPath(e.target.value)}
                  placeholder="empty → Workflow default"
                />
              </label>
              <label className="field">
                Input overrides
                <textarea
                  value={inputOverrideText}
                  onChange={(e) => setInputOverrideText(e.target.value)}
                  rows={3}
                  placeholder="step.input=value per line"
                />
              </label>
              <label className="field">
                Argument overrides
                <textarea
                  value={argOverrideText}
                  onChange={(e) => setArgOverrideText(e.target.value)}
                  rows={3}
                  placeholder="processId=arg1,arg2 per line"
                />
              </label>
            </section>

            <section className="panel-section">
              <h2>Rewind</h2>
              <label className="field">
                Step ID (click a canvas Step)
                <span className="inline-row">
                  <input value={rewindStepId} onChange={(e) => setRewindStepId(e.target.value)} />
                  <button
                    className="panel-btn danger"
                    disabled={busy || !run || !rewindStepId.trim()}
                    onClick={rewind}
                  >
                    Rewind
                  </button>
                </span>
              </label>
            </section>

            <section className="panel-section">
              <h2>WorkflowRun history</h2>
              {history.length === 0 ? (
                <p className="muted">No WorkflowRuns listed yet. Use List runs in the toolbar.</p>
              ) : (
                <ul className="history-list">
                  {history.map((h) => (
                    <li key={h.id}>
                      <button className="panel-btn" disabled={busy} onClick={() => reopen(h)}>
                        Open
                      </button>
                      <span>
                        {h.id} · {h.workflowId} · {h.status}
                      </span>
                    </li>
                  ))}
                </ul>
              )}
            </section>

            <section className="panel-section">
              <h2>Timeline</h2>
              {events.length === 0 ? (
                <p className="muted">Timeline events appear when a WorkflowRun is live.</p>
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
