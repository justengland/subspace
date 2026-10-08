import { useState } from "react";

const API = import.meta.env.VITE_API_URL ?? "http://localhost:8080";

type WorkflowRun = {
  id: string;
  workflowId: string;
  projectPath: string;
  status: string;
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
  defaultProject?: string;
  connections: Connection[];
};

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

export default function App() {
  const [workflowId, setWorkflowId] = useState("pipe");
  const [projectPath, setProjectPath] = useState("");
  const [inputOverrideText, setInputOverrideText] = useState("");
  const [argOverrideText, setArgOverrideText] = useState("");
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
      const wf: Workflow = await res.json();
      setWorkflow(wf);
      if (!projectPath && wf.defaultProject) setProjectPath(wf.defaultProject);
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
      const body: Record<string, unknown> = { workflowId };
      if (projectPath) body.projectPath = projectPath;
      const inputs = parseInputOverrides(inputOverrideText);
      if (Object.keys(inputs).length) body.inputOverrides = inputs;
      const args = parseArgOverrides(argOverrideText);
      if (Object.keys(args).length) body.argumentOverrides = args;
      const res = await fetch(`${API}/api/runs`, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(body),
      });
      if (!res.ok) throw new Error(await res.text());
      setRun(await res.json());
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
      </div>
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
    </main>
  );
}
