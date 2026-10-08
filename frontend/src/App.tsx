import { useEffect, useState } from "react";

const API = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

type WorkflowRun = {
  id: string;
  workflowId: string;
  projectPath: string;
  status: string;
};

type TimelineEvent = {
  type: string;
  ts?: string;
  data?: string;
  processId?: string;
  [key: string]: unknown;
};

function wsURL(path: string) {
  const base = API.replace(/^http/, "ws");
  return `${base}${path}`;
}

export default function App() {
  const [workflowId, setWorkflowId] = useState("series-hello");
  const [projectPath, setProjectPath] = useState("");
  const [run, setRun] = useState<WorkflowRun | null>(null);
  const [events, setEvents] = useState<TimelineEvent[]>([]);
  const [timelineKey, setTimelineKey] = useState(0);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (!run?.id) return;
    const ws = new WebSocket(wsURL(`/api/runs/${run.id}/events`));
    ws.onmessage = (msg) => {
      const ev = JSON.parse(msg.data) as TimelineEvent;
      setEvents((prev) => [...prev, ev]);
      if (ev.type === "workflow_succeeded" || ev.type === "workflow_failed") {
        setRun((r) => (r ? { ...r, status: ev.type === "workflow_succeeded" ? "succeeded" : "failed" } : r));
      }
    };
    ws.onerror = () => setError("WebSocket error");
    return () => ws.close();
  }, [run?.id, timelineKey]);

  async function start() {
    setBusy(true);
    setError("");
    setEvents([]);
    try {
      const res = await fetch(`${API}/api/runs`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ workflowId, projectPath }),
      });
      if (!res.ok) throw new Error(await res.text());
      const body: WorkflowRun = await res.json();
      setRun(body);
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
      const res = await fetch(`${API}/api/runs/${run.id}`);
      if (!res.ok) throw new Error(await res.text());
      setRun(await res.json());
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

  return (
    <main style={{ fontFamily: "system-ui", maxWidth: 640, margin: "2rem auto" }}>
      <h1>Subspace</h1>
      <label>
        Workflow ID
        <input value={workflowId} onChange={(e) => setWorkflowId(e.target.value)} style={{ display: "block", width: "100%" }} />
      </label>
      <label style={{ display: "block", marginTop: 8 }}>
        Project path
        <input value={projectPath} onChange={(e) => setProjectPath(e.target.value)} style={{ display: "block", width: "100%" }} />
      </label>
      <div style={{ marginTop: 12, display: "flex", gap: 8 }}>
        <button disabled={busy || !projectPath} onClick={start}>
          Start run
        </button>
        <button disabled={busy || !run} onClick={refresh}>
          Refresh status
        </button>
        <button disabled={!run} onClick={reconnect}>
          Reconnect timeline
        </button>
      </div>
      {error && <p style={{ color: "crimson" }}>{error}</p>}
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
