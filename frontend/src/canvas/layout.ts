/** Optional stored Visualization position (never written by UI in phase 1). */
export type VizPos = { x: number; y: number };

export type LayoutNode = {
  id: string;
  label: string;
  x: number;
  y: number;
  w: number;
  h: number;
};

const NODE_W = 140;
const NODE_H = 56;
const GAP_X = 48;
const GAP_Y = 24;
const ORIGIN_X = 24;
const ORIGIN_Y = 24;

/**
 * Place Steps: use Visualization when present, else left-to-right by steps[] order.
 * ponytail: linear row; layered graph layout if graphs get wide.
 */
export function layoutSteps(
  steps: { id: string; name: string; visualization?: VizPos }[],
): LayoutNode[] {
  return steps.map((s, i) => {
    const x = s.visualization?.x ?? ORIGIN_X + i * (NODE_W + GAP_X);
    const y = s.visualization?.y ?? ORIGIN_Y + (i % 2) * (NODE_H + GAP_Y);
    return { id: s.id, label: s.name || s.id, x, y, w: NODE_W, h: NODE_H };
  });
}
