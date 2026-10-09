import { useRef, useState, type PointerEvent as ReactPointerEvent } from "react";
import type { Connection, StepView, Visualization } from "../api/client";
import { layoutSteps, MIN_H, MIN_W, type LayoutNode } from "./layout";

export type StepStatus = "idle" | "running" | "succeeded" | "failed" | "stopped";

const HANDLE = 14;
const PORT = 5;

const STATUS_LABEL: Record<StepStatus, string> = {
  idle: "IDLE",
  running: "RUN",
  succeeded: "DONE",
  failed: "FAIL",
  stopped: "STOP",
};

const PILL_FILL: Record<StepStatus, string> = {
  idle: "#3a3a45",
  running: "color-mix(in srgb, var(--color-success) 30%, transparent)",
  succeeded: "color-mix(in srgb, var(--color-success) 25%, transparent)",
  failed: "color-mix(in srgb, var(--color-danger) 30%, transparent)",
  stopped: "color-mix(in srgb, var(--color-warn) 30%, transparent)",
};

const PILL_INK: Record<StepStatus, string> = {
  idle: "var(--color-muted)",
  running: "var(--color-success)",
  succeeded: "var(--color-success)",
  failed: "var(--color-danger)",
  stopped: "var(--color-warn)",
};

function modeAccent(mode: string): string {
  if (mode === "decision") return "var(--color-node-decision)";
  if (mode === "parallel") return "var(--color-node-parallel)";
  if (mode === "loop") return "var(--color-node-loop)";
  return "var(--color-node-series)";
}

type Side = "top" | "bottom" | "left" | "right";

/** Down/up when the target is mainly below or above; otherwise sideways. */
function linkSides(from: LayoutNode, to: LayoutNode): { from: Side; to: Side } {
  const dx = to.x + to.w / 2 - (from.x + from.w / 2);
  const dy = to.y + to.h / 2 - (from.y + from.h / 2);
  if (Math.abs(dy) >= Math.abs(dx)) {
    return dy >= 0 ? { from: "bottom", to: "top" } : { from: "top", to: "bottom" };
  }
  return dx >= 0 ? { from: "right", to: "left" } : { from: "left", to: "right" };
}

function portXY(n: LayoutNode, side: Side, out: number): { x: number; y: number } {
  if (side === "top") return { x: n.x + n.w / 2, y: n.y - out };
  if (side === "bottom") return { x: n.x + n.w / 2, y: n.y + n.h + out };
  if (side === "left") return { x: n.x - out, y: n.y + n.h / 2 };
  return { x: n.x + n.w + out, y: n.y + n.h / 2 };
}

function edgePath(from: LayoutNode, to: LayoutNode): {
  d: string;
  label: { x: number; y: number; anchor: "start" | "middle" };
} {
  const sides = linkSides(from, to);
  const a = portXY(from, sides.from, 0);
  const b = portXY(to, sides.to, 0);
  const vertical = sides.from === "top" || sides.from === "bottom";
  if (vertical) {
    const cy = (a.y + b.y) / 2;
    return {
      d: `M ${a.x} ${a.y} V ${cy} H ${b.x} V ${b.y}`,
      label: { x: Math.max(a.x, b.x) + 14, y: cy, anchor: "start" },
    };
  }
  const cx = (a.x + b.x) / 2;
  return {
    d: `M ${a.x} ${a.y} H ${cx} V ${b.y} H ${b.x}`,
    label: { x: cx, y: Math.min(a.y, b.y) - 12, anchor: "middle" },
  };
}

const PROC_ROW = 18;
const PROC_PAD = 10;

function openExtra(n: LayoutNode, isOpen: boolean): number {
  if (!isOpen || n.processes.length === 0) return 0;
  return n.processes.length * PROC_ROW + PROC_PAD;
}

/** Push later Steps down so an expanded Step doesn't cover them.
 * ponytail: O(n²) scan, fine until a Workflow has hundreds of Steps. */
function placeOpen(nodes: LayoutNode[], open: Record<string, boolean>): LayoutNode[] {
  return nodes.map((n) => {
    let push = 0;
    for (const o of nodes) {
      if (o.id === n.id) continue;
      if (o.y + o.h <= n.y + 0.5) push += openExtra(o, !!open[o.id]);
    }
    return { ...n, y: n.y + push, h: n.h + openExtra(n, !!open[n.id]) };
  });
}

type DragKind = "move" | "resize";

type DragState = {
  kind: DragKind;
  id: string;
  startX: number;
  startY: number;
  orig: LayoutNode;
};

type Props = {
  steps: StepView[];
  connections: Connection[];
  stepStatus: Record<string, StepStatus>;
  cursorStepId?: string;
  selectedProcess?: { stepId: string; processId: string };
  onSelectStep?: (stepId: string) => void;
  onSelectProcess?: (stepId: string, processId: string) => void;
  /** Fired when drag/resize ends with the Step's new Visualization. */
  onStepVisualizationChange?: (stepId: string, visualization: Visualization) => void;
};

export function Canvas({
  steps,
  connections,
  stepStatus,
  cursorStepId,
  selectedProcess,
  onSelectStep,
  onSelectProcess,
  onStepVisualizationChange,
}: Props) {
  const svgRef = useRef<SVGSVGElement>(null);
  const base = layoutSteps(steps);
  const [override, setOverride] = useState<Record<string, { x: number; y: number; w: number; h: number }>>(
    {},
  );
  const [drag, setDrag] = useState<DragState | null>(null);
  const [open, setOpen] = useState<Record<string, boolean>>({});

  const placed = base.map((n) => {
    const o = override[n.id];
    return o ? { ...n, ...o } : n;
  });
  const placedById = new Map(placed.map((n) => [n.id, n]));
  const nodes = placeOpen(placed, open);
  const byId = new Map(nodes.map((n) => [n.id, n]));
  const width = Math.max(480, ...nodes.map((n) => n.x + n.w + 24));
  const height = Math.max(160, ...nodes.map((n) => n.y + n.h + 24));

  function svgPoint(e: ReactPointerEvent): { x: number; y: number } {
    const svg = svgRef.current;
    if (!svg) return { x: 0, y: 0 };
    const pt = svg.createSVGPoint();
    pt.x = e.clientX;
    pt.y = e.clientY;
    const ctm = svg.getScreenCTM();
    if (!ctm) return { x: 0, y: 0 };
    const p = pt.matrixTransform(ctm.inverse());
    return { x: p.x, y: p.y };
  }

  function beginDrag(kind: DragKind, n: LayoutNode, e: ReactPointerEvent) {
    if (!onStepVisualizationChange) return;
    e.stopPropagation();
    e.preventDefault();
    (e.target as Element).setPointerCapture?.(e.pointerId);
    const p = svgPoint(e);
    setDrag({ kind, id: n.id, startX: p.x, startY: p.y, orig: n });
    onSelectStep?.(n.id);
  }

  function onPointerMove(e: ReactPointerEvent) {
    if (!drag) return;
    const p = svgPoint(e);
    const dx = p.x - drag.startX;
    const dy = p.y - drag.startY;
    if (drag.kind === "move") {
      setOverride((prev) => ({
        ...prev,
        [drag.id]: {
          x: Math.max(0, drag.orig.x + dx),
          y: Math.max(0, drag.orig.y + dy),
          w: drag.orig.w,
          h: drag.orig.h,
        },
      }));
      return;
    }
    setOverride((prev) => ({
      ...prev,
      [drag.id]: {
        x: drag.orig.x,
        y: drag.orig.y,
        w: Math.max(MIN_W, drag.orig.w + dx),
        h: Math.max(MIN_H, drag.orig.h + dy),
      },
    }));
  }

  function onPointerUp() {
    if (!drag) return;
    const n = placedById.get(drag.id);
    const d = drag;
    setDrag(null);
    if (!n || !onStepVisualizationChange) return;
    onStepVisualizationChange(d.id, {
      position: { x: n.x, y: n.y },
      size: { width: n.w, height: n.h },
    });
  }

  return (
    <svg
      ref={svgRef}
      width="100%"
      viewBox={`0 0 ${width} ${height}`}
      style={{ display: "block", background: "transparent", minHeight: 180, touchAction: "none" }}
      role="img"
      aria-label="Workflow canvas"
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onPointerCancel={onPointerUp}
    >
      {connections.map((c, i) => {
        const from = byId.get(c.sourceStepId);
        const to = byId.get(c.targetStepId);
        if (!from || !to) return null;
        const path = edgePath(from, to);
        const wire = c.sourceOutput && c.sourceOutput !== c.targetInput ? `${c.sourceOutput} → ${c.targetInput}` : c.sourceOutput;
        return (
          <g key={i}>
            <path className="wf-edge" d={path.d} />
            {wire && (
              <text
                x={path.label.x}
                y={path.label.y}
                fill="var(--color-muted)"
                fontSize={10}
                fontFamily="var(--font-sans)"
                textAnchor={path.label.anchor}
                dominantBaseline="middle"
              >
                {wire}
              </text>
            )}
            <title>
              {c.sourceStepId}.{c.sourceOutput} → {c.targetStepId}.{c.targetInput}
            </title>
          </g>
        );
      })}
      <defs>
        {(["series", "decision", "parallel", "loop"] as const).map((m) => (
          <linearGradient key={m} id={`wash-${m}`} x1="0" y1="0" x2="1" y2="0">
            <stop offset="0" stopColor={`var(--color-node-${m})`} stopOpacity={0.34} />
            <stop offset="0.72" stopColor={`var(--color-node-${m})`} stopOpacity={0} />
          </linearGradient>
        ))}
        <filter id="node-shadow" x="-30%" y="-40%" width="160%" height="180%">
          <feDropShadow dx="0" dy="2" stdDeviation="3" floodColor="#000" floodOpacity="0.35" />
        </filter>
      </defs>
      {nodes.map((n) => {
        const status = stepStatus[n.id] ?? "idle";
        const isCursor = cursorStepId === n.id;
        const mode = (steps.find((s) => s.id === n.id)?.mode || "series").toLowerCase();
        const accent = modeAccent(mode);
        const wash = mode === "decision" || mode === "parallel" || mode === "loop" ? mode : "series";
        const collapsed = placedById.get(n.id) ?? n;
        const headerH = collapsed.h;
        const isOpen = !!open[n.id] && n.processes.length > 0;
        const canToggle = n.processes.length > 0;
        const rx = isOpen ? 16 : headerH / 2;
        const statusLabel = STATUS_LABEL[status];
        const pillW = statusLabel.length * 5.6 + 12;
        const pillH = 16;
        const toggleW = canToggle ? 30 : 0;
        const toggleX = n.w - 8 - toggleW;
        const pillX = (canToggle ? toggleX - 6 : n.w - 14) - pillW;
        const pillY = (headerH - pillH) / 2;
        const showIf = mode === "decision";
        const ifW = showIf ? 22 : 0;
        const ifX = pillX - (showIf ? 6 + ifW : 0);
        const modeLabel = mode.toUpperCase();
        const modeW = modeLabel.length * 6.6;
        const anchorX = showIf ? ifX : pillX;
        const titleX = 28;
        const modeX = anchorX - 8 - modeW;
        const showMode = modeX - 6 - titleX >= n.label.length * 6.3;
        const titleW = Math.max(20, (showMode ? modeX - 6 : anchorX - 8) - titleX);
        const ports: Side[] = ["top", "bottom", "left", "right"];
        return (
          <g
            key={n.id}
            transform={`translate(${n.x},${n.y})`}
            style={{ cursor: onStepVisualizationChange ? "grab" : onSelectStep ? "pointer" : "default" }}
            onPointerDown={(e) => {
              if (onStepVisualizationChange) beginDrag("move", collapsed, e);
              else onSelectStep?.(n.id);
            }}
            onClick={() => onSelectStep?.(n.id)}
          >
            <rect width={n.w} height={n.h} rx={rx} fill="var(--color-panel)" filter="url(#node-shadow)" />
            <clipPath id={`card-${n.id}`}>
              <rect width={n.w} height={n.h} rx={rx} />
            </clipPath>
            <g clipPath={`url(#card-${n.id})`}>
              <rect
                width={n.w}
                height={isOpen ? headerH : n.h}
                fill={isOpen ? `color-mix(in srgb, ${accent} 55%, var(--color-panel))` : "var(--color-panel)"}
              />
              {!isOpen && <rect width={n.w} height={n.h} fill={`url(#wash-${wash})`} />}
              {isOpen && (
                <rect y={headerH} width={n.w} height={Math.max(0, n.h - headerH)} fill="var(--color-canvas)" />
              )}
            </g>
            <rect
              width={n.w}
              height={n.h}
              rx={rx}
              fill="none"
              stroke={isOpen || isCursor ? accent : "var(--color-border)"}
              strokeWidth={isOpen || isCursor ? 1.5 : 1}
            />
            {isOpen && (
              <line
                x1={12}
                x2={n.w - 12}
                y1={headerH}
                y2={headerH}
                stroke={accent}
                strokeOpacity={0.45}
              />
            )}
            <circle cx={16} cy={headerH / 2} r={5} fill={accent} />
            <clipPath id={`title-${n.id}`}>
              <rect x={titleX} y={0} width={titleW} height={headerH} />
            </clipPath>
            <text
              x={titleX}
              y={headerH / 2}
              clipPath={`url(#title-${n.id})`}
              fontSize={13}
              fontWeight={600}
              fill="var(--color-ink)"
              fontFamily="var(--font-sans)"
              dominantBaseline="central"
            >
              {n.label}
            </text>
            {showMode && (
              <text
                x={modeX}
                y={headerH / 2}
                fontSize={10}
                fontWeight={600}
                letterSpacing={1.2}
                fill="var(--color-muted)"
                fontFamily="var(--font-sans)"
                dominantBaseline="central"
              >
                {modeLabel}
              </text>
            )}
            {showIf && (
              <g>
                <rect
                  x={ifX}
                  y={pillY}
                  width={ifW}
                  height={pillH}
                  rx={3}
                  fill="color-mix(in srgb, var(--color-node-decision) 25%, transparent)"
                />
                <text
                  x={ifX + ifW / 2}
                  y={headerH / 2}
                  textAnchor="middle"
                  fontSize={9}
                  fontWeight={700}
                  fill="var(--color-node-decision)"
                  fontFamily="var(--font-sans)"
                  dominantBaseline="central"
                >
                  IF
                </text>
              </g>
            )}
            <rect x={pillX} y={pillY} width={pillW} height={pillH} rx={3} fill={PILL_FILL[status]} />
            <text
              x={pillX + pillW / 2}
              y={headerH / 2}
              textAnchor="middle"
              fontSize={9}
              fontWeight={700}
              fill={PILL_INK[status]}
              fontFamily="var(--font-sans)"
              dominantBaseline="central"
            >
              {statusLabel}
            </text>
            {canToggle && (
              <g
                style={{ cursor: "pointer" }}
                onPointerDown={(e) => {
                  e.stopPropagation();
                  e.preventDefault();
                  setOpen((s) => ({ ...s, [n.id]: !s[n.id] }));
                }}
                onClick={(e) => e.stopPropagation()}
              >
                <title>{isOpen ? "Collapse step" : "Expand step"}</title>
                <rect x={toggleX} y={(headerH - 22) / 2} width={toggleW} height={22} fill="transparent" />
                <text
                  x={toggleX + toggleW / 2}
                  y={headerH / 2}
                  textAnchor="middle"
                  fontSize={12}
                  fontWeight={700}
                  fill={accent}
                  fontFamily="var(--font-mono)"
                  dominantBaseline="central"
                >
                  {isOpen ? "[-]" : "[+]"}
                </text>
              </g>
            )}
            {ports.map((side) => {
              const c =
                side === "top"
                  ? { cx: n.w / 2, cy: 0 }
                  : side === "bottom"
                    ? { cx: n.w / 2, cy: n.h }
                    : side === "left"
                      ? { cx: 0, cy: n.h / 2 }
                      : { cx: n.w, cy: n.h / 2 };
              return (
                <circle
                  key={side}
                  cx={c.cx}
                  cy={c.cy}
                  r={PORT}
                  fill="var(--color-panel)"
                  stroke="var(--color-edge)"
                  strokeWidth={2}
                />
              );
            })}
            {onStepVisualizationChange && (
              <rect
                x={n.w - HANDLE}
                y={n.h - HANDLE}
                width={HANDLE}
                height={HANDLE}
                fill="transparent"
                style={{ cursor: "nwse-resize" }}
                onPointerDown={(e) => beginDrag("resize", collapsed, e)}
              />
            )}
            {isOpen &&
              n.processes.map((p, i) => {
                const rowY = headerH + 6 + i * PROC_ROW;
                const selected =
                  selectedProcess?.stepId === n.id && selectedProcess.processId === p.id;
                return (
                  <g
                    key={p.id}
                    style={{ cursor: onSelectProcess ? "pointer" : undefined }}
                    onPointerDown={(e) => {
                      if (!onSelectProcess) return;
                      e.stopPropagation();
                      e.preventDefault();
                      onSelectProcess(n.id, p.id);
                    }}
                    onClick={(e) => {
                      if (onSelectProcess) e.stopPropagation();
                    }}
                  >
                    <title>Edit {p.name || p.id}</title>
                    <rect
                      x={8}
                      y={rowY}
                      width={Math.max(0, n.w - 16)}
                      height={PROC_ROW}
                      rx={4}
                      fill={selected ? `color-mix(in srgb, ${accent} 28%, transparent)` : "transparent"}
                      clipPath={`url(#procs-${n.id})`}
                    />
                    <text
                      x={16}
                      y={rowY + PROC_ROW / 2}
                      fontSize={12}
                      fontWeight={selected ? 600 : 400}
                      fill={selected ? accent : "var(--color-ink)"}
                      fontFamily="var(--font-sans)"
                      dominantBaseline="central"
                      clipPath={`url(#procs-${n.id})`}
                    >
                      {p.name || p.id}
                    </text>
                  </g>
                );
              })}
            {isOpen && (
              <clipPath id={`procs-${n.id}`}>
                <rect x={12} y={headerH} width={Math.max(0, n.w - 24)} height={n.h - headerH} />
              </clipPath>
            )}
          </g>
        );
      })}
    </svg>
  );
}
