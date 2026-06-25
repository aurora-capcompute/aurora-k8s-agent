import { useMemo } from "react";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";
import dagre from "dagre";
import type { RunGraphNode, RunStatus } from "../types";

const STATUS_COLOR: Record<string, string> = {
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

const NODE_W = 210;
const NODE_H = 64;

function color(status: RunStatus): string {
  return STATUS_COLOR[status] ?? "#455a64";
}

function build(
  root: RunGraphNode,
  selected: string | null,
): { nodes: Node[]; edges: Edge[] } {
  const g = new dagre.graphlib.Graph();
  g.setGraph({ rankdir: "LR", nodesep: 24, ranksep: 64 });
  g.setDefaultEdgeLabel(() => ({}));

  const nodes: Node[] = [];
  const edges: Edge[] = [];

  const walk = (n: RunGraphNode) => {
    g.setNode(n.run_id, { width: NODE_W, height: NODE_H });
    nodes.push({
      id: n.run_id,
      position: { x: 0, y: 0 },
      data: {
        label: (
          <div style={{ textAlign: "left" }}>
            <div
              style={{
                fontWeight: 600,
                fontSize: 12,
                overflow: "hidden",
                textOverflow: "ellipsis",
                whiteSpace: "nowrap",
              }}
            >
              {n.run_id}
            </div>
            <div style={{ fontSize: 11, color: color(n.status) }}>
              {n.status} · rev {n.revision} · try {n.attempt}
            </div>
          </div>
        ),
      },
      style: {
        border: `2px solid ${color(n.status)}`,
        outline: n.run_id === selected ? "3px solid #1565c0" : "none",
        boxShadow:
          n.run_id === selected ? "0 0 0 3px rgba(21,101,192,0.2)" : "none",
        borderRadius: 8,
        padding: 8,
        background: "#fff",
        width: NODE_W,
        cursor: "pointer",
      },
    });
    for (const child of n.children ?? []) {
      g.setEdge(n.run_id, child.run_id);
      edges.push({
        id: `${n.run_id}->${child.run_id}`,
        source: n.run_id,
        target: child.run_id,
        animated: child.status === "running",
        markerEnd: { type: MarkerType.ArrowClosed },
      });
      walk(child);
    }
  };
  walk(root);

  dagre.layout(g);
  for (const node of nodes) {
    const pos = g.node(node.id);
    node.position = { x: pos.x - NODE_W / 2, y: pos.y - NODE_H / 2 };
  }
  return { nodes, edges };
}

export function CallGraph({
  root,
  selected,
  onSelect,
}: {
  root: RunGraphNode;
  selected?: string | null;
  onSelect?: (runID: string) => void;
}) {
  const { nodes, edges } = useMemo(
    () => build(root, selected ?? null),
    [root, selected],
  );
  return (
    <div style={{ height: "100%", minHeight: 360 }}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        fitView
        nodesDraggable={false}
        onNodeClick={(_, node) => onSelect?.(node.id)}
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
