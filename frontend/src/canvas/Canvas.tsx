import type { Connection, StepView } from "../api/client";
import { layoutSteps } from "./layout";

export type StepStatus = "idle" | "running" | "succeeded" | "failed" | "stopped";

const STATUS_FILL: Record<StepStatus, string> = {
  idle: "var(--color-panel-2)",
  running: "color-mix(in srgb, var(--color-node-jev) 35%, var(--color-panel))",
  succeeded: "color-mix(in srgb, var(--color-success) 28%, var(--color-panel))",
  failed: "color-mix(in srgb, var(--color-danger) 28%, var(--color-panel))",
  stopped: "color-mix(in srgb, var(--color-warn) 28%, var(--color-panel))",
};

type Props = {
  steps: StepView[];
  connections: Connection[];
  stepStatus: Record<string, StepStatus>;
  cursorStepId?: string;
  onSelectStep?: (stepId: string) => void;
};

export function Canvas({ steps, connections, stepStatus, cursorStepId, onSelectStep }: Props) {
  const nodes = layoutSteps(steps);
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const width = Math.max(480, ...nodes.map((n) => n.x + n.w + 24));
  const height = Math.max(160, ...nodes.map((n) => n.y + n.h + 24));

  return (
    <svg
      width="100%"
      viewBox={`0 0 ${width} ${height}`}
      style={{ display: "block", background: "transparent", minHeight: 180 }}
      role="img"
      aria-label="Workflow canvas"
    >
      {connections.map((c, i) => {
        const from = byId.get(c.sourceStepId);
        const to = byId.get(c.targetStepId);
        if (!from || !to) return null;
        const x1 = from.x + from.w;
        const y1 = from.y + from.h / 2;
        const x2 = to.x;
        const y2 = to.y + to.h / 2;
        const mx = (x1 + x2) / 2;
        return (
          <g key={i}>
            <path
              className="wf-edge"
              d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
              fill="none"
              stroke="var(--color-edge)"
              strokeWidth={2}
              markerEnd="url(#arrow)"
            />
            <title>
              {c.sourceStepId}.{c.sourceOutput} → {c.targetStepId}.{c.targetInput}
            </title>
          </g>
        );
      })}
      <defs>
        <marker id="arrow" markerWidth="8" markerHeight="8" refX="6" refY="3" orient="auto">
          <path d="M0,0 L6,3 L0,6 Z" fill="var(--color-edge)" />
        </marker>
      </defs>
      {nodes.map((n) => {
        const status = stepStatus[n.id] ?? "idle";
        const isCursor = cursorStepId === n.id;
        return (
          <g
            key={n.id}
            transform={`translate(${n.x},${n.y})`}
            style={{ cursor: onSelectStep ? "pointer" : "default" }}
            onClick={() => onSelectStep?.(n.id)}
          >
            <rect
              width={n.w}
              height={n.h}
              rx={4}
              fill={STATUS_FILL[status]}
              stroke={isCursor ? "var(--color-accent)" : "var(--color-border)"}
              strokeWidth={isCursor ? 3 : 1}
            />
            <text
              x={n.w / 2}
              y={n.h / 2 - 6}
              textAnchor="middle"
              fontSize={12}
              fill="var(--color-ink)"
              fontFamily="var(--font-sans)"
            >
              {n.label}
            </text>
            <text
              x={n.w / 2}
              y={n.h / 2 + 12}
              textAnchor="middle"
              fontSize={10}
              fill="var(--color-muted)"
              fontFamily="var(--font-mono)"
            >
              {n.id} · {status}
            </text>
          </g>
        );
      })}
    </svg>
  );
}
