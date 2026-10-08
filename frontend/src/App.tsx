import { useEffect, useState } from "react";
import { API_BASE, api, type StartRequest, type TimelineEvent, type Workflow, type WorkflowRun } from "./api/client";

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
    };
    ws.onerror = () => setError("WebSocket error");
    return () => ws.close();
  }, [run?.id, timelineKey]);

  async function loadWiring() {
    setBusy(true);
    setError("");
    try {
      const { data, error: err } = await api.GET("/api/workflows/{id}", {
        params: { path: { id: workflowId } },
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
    <main style={{ fontFamily: "system-ui", maxWidth: 640, margin: "2rem auto" }}>
      <h1>Subspace</h1>
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
        <button disabled={busy} onClick={loadWiring}>
          Show wiring
        </button>
        <button disabled={busy} onClick={start}>
          Start run
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
        Rewind to Step ID
        <span style={{ display: "flex", gap: 8, marginTop: 4 }}>
          <input value={rewindStepId} onChange={(e) => setRewindStepId(e.target.value)} style={{ flex: 1 }} />
          <button disabled={busy || !run || !rewindStepId.trim()} onClick={rewind}>
            Rewind
          </button>
        </span>
      </label>
      {error && <p style={{ color: "crimson" }}>{error}</p>}
      {workflow && (
        <section style={{ marginTop: 16 }}>
          <h2 style={{ fontSize: "1rem" }}>Wiring ({workflow.id})</h2>
          {workflow.defaultProject && <p>defaultProject: {workflow.defaultProject}</p>}
          {workflow.connections.length === 0 ? (
            <p>No connections</p>
          ) : (
            <ul>
              {workflow.connections.map((c, i) => (
                <li key={i}>
                  {c.sourceStepId}.{c.sourceOutput} → {c.targetStepId}.{c.targetInput}
                </li>
              ))}
            </ul>
          )}
        </section>
      )}
      {run && (
        <pre style={{ background: "#f4f4f4", padding: 12, marginTop: 16 }}>
          {JSON.stringify(run, null, 2)}
        </pre>
      )}
      {events.length > 0 && (
        <section style={{ marginTop: 16 }}>
          <h2 style={{ fontSize: "1rem" }}>Timeline</h2>
          <ol style={{ listStyle: "none", padding: 0, margin: 0, fontFamily: "ui-monospace, monospace", fontSize: 13 }}>
            {events.map((ev, i) => (
              <li key={i} style={{ borderBottom: "1px solid #ddd", padding: "4px 0" }}>
                <span style={{ color: "#666" }}>{ev.type}</span>
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
