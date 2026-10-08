import type { Connection, StepView } from "../api/client";
import { layoutSteps } from "./layout";

export type StepStatus = "idle" | "running" | "succeeded" | "failed" | "stopped";

const STATUS_FILL: Record<StepStatus, string> = {
  idle: "#e8e8e8",
  running: "#cde4ff",
  succeeded: "#c8e6c9",
  failed: "#ffcdd2",
  stopped: "#ffe0b2",
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
      style={{ display: "block", background: "#fafafa", border: "1px solid #ccc", minHeight: 180 }}
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
              d={`M ${x1} ${y1} C ${mx} ${y1}, ${mx} ${y2}, ${x2} ${y2}`}
              fill="none"
              stroke="#666"
              strokeWidth={1.5}
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
          <path d="M0,0 L6,3 L0,6 Z" fill="#666" />
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
              stroke={isCursor ? "#1565c0" : "#333"}
              strokeWidth={isCursor ? 3 : 1}
            />
            <text x={n.w / 2} y={n.h / 2 - 6} textAnchor="middle" fontSize={12} fontFamily="system-ui">
              {n.label}
            </text>
            <text x={n.w / 2} y={n.h / 2 + 12} textAnchor="middle" fontSize={10} fill="#555" fontFamily="system-ui">
              {n.id} · {status}
            </text>
          </g>
        );
      })}
    </svg>
  );
}
