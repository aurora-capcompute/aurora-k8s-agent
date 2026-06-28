import dagre from "dagre";
import { type Edge, type Node } from "reactflow";
// Shared layout + colour helpers for the call graph (per-run) and the thread
// graph (whole-thread DAG). Both render reactflow nodes/edges laid out with
// dagre, so the wiring lives here once.

export const STATUS_COLOR: Record<string, string> = {
  completed: "#2e7d32",
  failed: "#c62828",
  running: "#1565c0",
  queued: "#6a1b9a",
  yielded: "#ef6c00",
  waiting_task: "#ef6c00",
  stopping: "#ef6c00",
  stopped: "#455a64",
  interrupted: "#455a64",
};

export function statusColor(status: string): string {
  return STATUS_COLOR[status] ?? "#455a64";
}

export const NODE_W = 210;
export const NODE_H = 64;

// layout positions nodes left-to-right with dagre and returns them with
// reactflow-style top-left positions. Edges are passed through unchanged but
// fed to dagre so ranks honour the connectivity.
export function layout(
  nodes: Node[],
  edges: Edge[],
  dir: "LR" | "TB" = "LR",
  nodeW = NODE_W,
  nodeH = NODE_H,
): Node[] {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: dir, nodesep: 20, ranksep: 48 });
  g.setDefaultEdgeLabel(() => ({}));
  for (const node of nodes) {
    g.setNode(node.id, { width: nodeW, height: nodeH });
  }
  for (const edge of edges) {
    g.setEdge(edge.source, edge.target);
  }
  dagre.layout(g);
  return nodes.map((node) => {
    const pos = g.node(node.id);
    return {
      ...node,
      position: { x: pos.x - nodeW / 2, y: pos.y - nodeH / 2 },
    };
  });
}

// nodeStyle is the shared box styling; selected nodes get a focus ring.
export function nodeStyle(
  status: string,
  selected: boolean,
): React.CSSProperties {
  return {
    border: `2px solid ${statusColor(status)}`,
    outline: selected ? "3px solid #1565c0" : "none",
    boxShadow: selected ? "0 0 0 3px rgba(21,101,192,0.2)" : "none",
    borderRadius: 8,
    padding: 8,
    background: "#fff",
    width: NODE_W,
    cursor: "pointer",
  };
}
