/** Optional stored Visualization. */
export type Viz = {
  position?: { x: number; y: number };
  size?: { width: number; height: number };
};

export type LayoutProcess = { id: string; name: string };

export type LayoutNode = {
  id: string;
  label: string;
  x: number;
  y: number;
  w: number;
  h: number;
  processes: LayoutProcess[];
};

export const NODE_W = 300;
export const NODE_H = 46;
const GAP_Y = 56;
/** Width when a graph already has saved positions, so pills don't collide. */
const PLACED_W = 168;
const ORIGIN_X = 24;
const ORIGIN_Y = 36;
/** Min canvas width; a fresh column sits in the middle of it. */
const CANVAS_W = 480;

export const MIN_W = 160;
export const MIN_H = 40;

/**
 * Place Steps: use Visualization when present, else a top-to-bottom column.
 * ponytail: linear column; layered graph layout if branches get wide.
 */
export function layoutSteps(
  steps: {
    id: string;
    name: string;
    visualization?: Viz;
    processes?: LayoutProcess[];
  }[],
): LayoutNode[] {
  const anyPos = steps.some((s) => s.visualization?.position);
  const defaultX = anyPos ? ORIGIN_X : Math.max(ORIGIN_X, (CANVAS_W - NODE_W) / 2);
  let cursorY = ORIGIN_Y;
  return steps.map((s) => {
    const processes = s.processes ?? [];
    const w = s.visualization?.size?.width ?? (anyPos ? PLACED_W : NODE_W);
    const h = s.visualization?.size?.height ?? NODE_H;
    const hasPos = s.visualization?.position != null;
    const x = s.visualization?.position?.x ?? defaultX;
    const y = s.visualization?.position?.y ?? cursorY;
    if (!hasPos) cursorY = y + h + GAP_Y;
    return { id: s.id, label: s.name || s.id, x, y, w, h, processes };
  });
}
