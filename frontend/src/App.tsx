import { useState } from "react";

const API = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

type ProcessRun = {
  processId: string;
  status: string;
  exitCode?: number;
};

type WorkflowRun = {
  id: string;
  workflowId: string;
  projectPath: string;
  status: string;
  processRuns?: ProcessRun[];
};

type Connection = {
  sourceStepId: string;
  sourceOutput: string;
  targetStepId: string;
  targetInput: string;
};

type Workflow = {
  id: string;
  name: string;
  connections: Connection[];
};

export default function App() {
  const [workflowId, setWorkflowId] = useState("pipe");
  const [projectPath, setProjectPath] = useState("");
  const [run, setRun] = useState<WorkflowRun | null>(null);
  const [workflow, setWorkflow] = useState<Workflow | null>(null);
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function loadWiring() {
    setBusy(true);
    setError("");
    try {
      const res = await fetch(`${API}/api/workflows/${workflowId}`);
      if (!res.ok) throw new Error(await res.text());
      setWorkflow(await res.json());
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  }

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
        <button disabled={busy} onClick={loadWiring}>
          Show wiring
        </button>
        <button disabled={busy || !projectPath} onClick={start}>
          Start run
        </button>
        <button disabled={busy || !run} onClick={refresh}>
          Refresh status
        </button>
      </div>
      {error && <p style={{ color: "crimson" }}>{error}</p>}
      {workflow && (
        <section style={{ marginTop: 16 }}>
          <h2 style={{ fontSize: "1rem" }}>Wiring ({workflow.id})</h2>
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
    </main>
  );
}
