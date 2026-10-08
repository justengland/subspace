import { useState } from "react";

const API = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

type WorkflowRun = {
  id: string;
  workflowId: string;
  projectPath: string;
  status: string;
};

export default function App() {
  const [workflowId, setWorkflowId] = useState("series-hello");
  const [projectPath, setProjectPath] = useState("");
  const [run, setRun] = useState<WorkflowRun | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function start() {
    setBusy(true);
    setError("");
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

  async function control(action: "pause" | "resume" | "stop") {
    if (!run) return;
    setBusy(true);
    setError("");
    try {
      const res = await fetch(`${API}/api/runs/${run.id}/${action}`, { method: "POST" });
      if (!res.ok) throw new Error(await res.text());
      setRun(await res.json());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

  return (
    <main style={{ fontFamily: "system-ui", maxWidth: 480, margin: "2rem auto" }}>
      <h1>Subspace</h1>
      <label>
        Workflow ID
        <input value={workflowId} onChange={(e) => setWorkflowId(e.target.value)} style={{ display: "block", width: "100%" }} />
      </label>
      <label style={{ display: "block", marginTop: 8 }}>
        Project path
        <input value={projectPath} onChange={(e) => setProjectPath(e.target.value)} style={{ display: "block", width: "100%" }} />
      </label>
      <div style={{ marginTop: 12, display: "flex", gap: 8, flexWrap: "wrap" }}>
        <button disabled={busy || !projectPath} onClick={start}>
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
      </div>
      {error && <p style={{ color: "crimson" }}>{error}</p>}
      {run && (
        <pre style={{ background: "#f4f4f4", padding: 12, marginTop: 16 }}>
          {JSON.stringify(run, null, 2)}
        </pre>
      )}
    </main>
  );
}
