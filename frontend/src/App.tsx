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

/** Derive Step status from timeline + WorkflowRun.stepRuns (UI never writes YAML). */
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

  return (
    <main style={{ fontFamily: "system-ui", maxWidth: 960, margin: "1.5rem auto", padding: "0 1rem" }}>
      <h1>Subspace</h1>
      <p style={{ color: "#555", marginTop: -8 }}>Canvas debugger (phase 1 — definition YAML is read-only)</p>

      {workflow && (
        <section style={{ marginTop: 12 }}>
          <h2 style={{ fontSize: "1rem", marginBottom: 8 }}>
            Canvas · {workflow.id}
            {run ? ` · run ${run.id} (${run.status})` : ""}
          </h2>
          <Canvas
            steps={workflow.steps}
            connections={workflow.connections}
            stepStatus={stepStatus}
            cursorStepId={run?.cursorStepId}
            onSelectStep={setRewindStepId}
          />
        </section>
      )}

      <label>
        Workflow ID
        <input value={workflowId} onChange={(e) => setWorkflowId(e.target.value)} style={{ display: "block", width: "100%" }} />
      </label>
      <label style={{ display: "block", marginTop: 8 }}>
        Project path (override; empty uses Workflow default)
        <input value={projectPath} onChange={(e) => setProjectPath(e.target.value)} style={{ display: "block", width: "100%" }} />
      </label>
      <label style={{ display: "block", marginTop: 8 }}>
        Input overrides (step.input=value per line)
        <textarea value={inputOverrideText} onChange={(e) => setInputOverrideText(e.target.value)} rows={3} style={{ display: "block", width: "100%" }} />
      </label>
      <label style={{ display: "block", marginTop: 8 }}>
        Argument overrides (processId=arg1,arg2 per line)
        <textarea value={argOverrideText} onChange={(e) => setArgOverrideText(e.target.value)} rows={3} style={{ display: "block", width: "100%" }} />
      </label>
      <div style={{ marginTop: 12, display: "flex", gap: 8, flexWrap: "wrap" }}>
        <button disabled={busy} onClick={() => loadWorkflow()}>
          Load canvas
        </button>
        <button disabled={busy} onClick={start}>
          Start
        </button>
        <button disabled={busy || !run} onClick={refresh}>
          Refresh status
        </button>
        <button disabled={busy || !run} onClick={() => control("pause")}>
          Pause
        </button>
        <button disabled={busy || !run} onClick={() => control("resume")}>
          Resume
        </button>
        <button disabled={busy || !run} onClick={() => control("stop")}>
          Stop
        </button>
        <button disabled={!run} onClick={reconnect}>
          Reconnect timeline
        </button>
      </div>
      <label style={{ display: "block", marginTop: 8 }}>
        Rewind to Step ID (click a canvas Step to fill)
        <span style={{ display: "flex", gap: 8, marginTop: 4 }}>
          <input value={rewindStepId} onChange={(e) => setRewindStepId(e.target.value)} style={{ flex: 1 }} />
          <button disabled={busy || !run || !rewindStepId.trim()} onClick={rewind}>
            Rewind
          </button>
        </span>
      </label>
      {error && <p style={{ color: "crimson" }}>{error}</p>}
      {events.length > 0 && (
        <section style={{ marginTop: 16 }}>
          <h2 style={{ fontSize: "1rem" }}>Timeline</h2>
          <ol style={{ listStyle: "none", padding: 0, margin: 0, fontFamily: "ui-monospace, monospace", fontSize: 13, maxHeight: 240, overflow: "auto" }}>
            {events.map((ev, i) => (
              <li key={i} style={{ borderBottom: "1px solid #ddd", padding: "4px 0" }}>
                <span style={{ color: "#666" }}>{ev.type}</span>
                {ev.stepId != null && <span> {ev.stepId}</span>}
                {ev.data != null && <span> {ev.data}</span>}
                {ev.processId != null && ev.data == null && <span> {ev.processId}</span>}
              </li>
            ))}
          </ol>
        </section>
      )}
    </main>
  );
}
