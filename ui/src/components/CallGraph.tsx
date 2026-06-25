import { useMemo } from "react";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";
import type { RunGraphNode } from "../types";
import { layout, nodeStyle, statusColor } from "./graph";

function build(
  root: RunGraphNode,
  selected: string | null,
): { nodes: Node[]; edges: Edge[] } {
  const nodes: Node[] = [];
  const edges: Edge[] = [];

  const walk = (n: RunGraphNode) => {
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
            <div style={{ fontSize: 11, color: statusColor(n.status) }}>
              {n.status} · rev {n.revision} · try {n.attempt}
            </div>
          </div>
        ),
      },
      style: nodeStyle(n.status, n.run_id === selected),
    });
    for (const child of n.children ?? []) {
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

  return { nodes: layout(nodes, edges), edges };
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
