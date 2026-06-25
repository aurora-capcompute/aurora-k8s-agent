import { useMemo } from "react";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";
import type { ThreadGraph } from "../types";
import { layout, nodeStyle, statusColor } from "./graph";

// ThreadGraphView renders the whole-thread DAG: one node per run, with
// delegation edges (parent → child run). Revision count is shown per node;
// clicking a node selects that run for drill-down into its call graph.
function build(
  graph: ThreadGraph,
  selected: string | null,
): { nodes: Node[]; edges: Edge[] } {
  const ids = new Set(graph.runs.map((r) => r.run_id));
  const nodes: Node[] = [];
  const edges: Edge[] = [];
  const linked = new Set<string>();

  const addEdge = (parent: string, child: string) => {
    const key = `${parent}->${child}`;
    if (linked.has(key) || !ids.has(parent) || !ids.has(child)) return;
    linked.add(key);
    edges.push({
      id: key,
      source: parent,
      target: child,
      markerEnd: { type: MarkerType.ArrowClosed },
    });
  };

  for (const run of graph.runs) {
    nodes.push({
      id: run.run_id,
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
              {run.run_id}
            </div>
            <div style={{ fontSize: 11, color: statusColor(run.status) }}>
              {run.status} · rev {run.current_revision} ·{" "}
              {run.revisions.length} rev
              {run.revisions.length === 1 ? "" : "s"}
            </div>
          </div>
        ),
      },
      style: nodeStyle(run.status, run.run_id === selected),
    });

    if (run.parent_run_id) addEdge(run.parent_run_id, run.run_id);
    for (const child of run.child_run_ids ?? []) addEdge(run.run_id, child);
  }

  return { nodes: layout(nodes, edges), edges };
}

export function ThreadGraphView({
  graph,
  selected,
  onSelect,
}: {
  graph: ThreadGraph;
  selected?: string | null;
  onSelect?: (runID: string) => void;
}) {
  const { nodes, edges } = useMemo(
    () => build(graph, selected ?? null),
    [graph, selected],
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
