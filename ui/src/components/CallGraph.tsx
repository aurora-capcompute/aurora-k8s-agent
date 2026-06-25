import { useMemo } from "react";
import ReactFlow, {
  Background,
  Controls,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";
import type { RunGraphNode, RunStatus } from "../types";

const STATUS_COLOR: Record<string, string> = {
  completed: "#2e7d32",
  failed: "#c62828",
  running: "#1565c0",
  queued: "#6a1b9a",
  yielded: "#ef6c00",
  waiting_task: "#ef6c00",
  stopped: "#455a64",
  interrupted: "#455a64",
};

function color(status: RunStatus): string {
  return STATUS_COLOR[status] ?? "#455a64";
}

function layout(root: RunGraphNode): { nodes: Node[]; edges: Edge[] } {
  const nodes: Node[] = [];
  const edges: Edge[] = [];
  let row = 0;
  const walk = (n: RunGraphNode, depth: number) => {
    nodes.push({
      id: n.run_id,
      position: { x: depth * 240, y: row * 90 },
      data: {
        label: (
          <div style={{ textAlign: "left" }}>
            <div style={{ fontWeight: 600 }}>{n.run_id}</div>
            <div style={{ fontSize: 11, color: color(n.status) }}>
              {n.status} · rev {n.revision}
            </div>
          </div>
        ),
      },
      style: {
        border: `2px solid ${color(n.status)}`,
        borderRadius: 8,
        padding: 8,
        background: "#fff",
        width: 200,
      },
    });
    row += 1;
    for (const child of n.children ?? []) {
      edges.push({
        id: `${n.run_id}->${child.run_id}`,
        source: n.run_id,
        target: child.run_id,
        animated: child.status === "running",
      });
      walk(child, depth + 1);
    }
  };
  walk(root, 0);
  return { nodes, edges };
}

export function CallGraph({ root }: { root: RunGraphNode }) {
  const { nodes, edges } = useMemo(() => layout(root), [root]);
  return (
    <div style={{ height: "100%", minHeight: 360 }}>
      <ReactFlow nodes={nodes} edges={edges} fitView nodesDraggable={false}>
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
