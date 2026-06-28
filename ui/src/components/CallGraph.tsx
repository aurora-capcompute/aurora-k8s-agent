import { useMemo } from "react";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";
import type { RevisionView } from "../types";
import { layout, statusColor } from "./graph";

const NODE_W = 220;
const NODE_H = 56;

function truncate(s: string, max = 28): string {
  return s.length > max ? s.slice(0, max - 1) + "…" : s;
}

function nid(revision: number, index: number): string {
  return `r${revision}e${index}`;
}

function build(revisions: RevisionView[]): { nodes: Node[]; edges: Edge[] } {
  const nodes: Node[] = [];
  const edges: Edge[] = [];

  for (const rev of revisions) {
    const entries = rev.entries ?? [];
    // Shared prefix indices [0, fork_offset) already appear in the parent
    // revision's nodes; only render new entries for this revision.
    const newFrom = rev.forked ? rev.fork_offset : 0;

    for (let i = newFrom; i < entries.length; i++) {
      const entry = entries[i];
      const id = nid(rev.revision, entry.index);
      const color = statusColor(entry.outcome.status);

      nodes.push({
        id,
        position: { x: 0, y: 0 },
        data: {
          label: (
            <div style={{ textAlign: "left" }}>
              <div
                style={{
                  fontWeight: 600,
                  fontSize: 11,
                  overflow: "hidden",
                  textOverflow: "ellipsis",
                  whiteSpace: "nowrap",
                  fontFamily: "ui-monospace, 'Cascadia Mono', monospace",
                }}
              >
                {truncate(entry.call.name)}
              </div>
              <div style={{ fontSize: 10, color, marginTop: 2 }}>
                r{rev.revision} · {entry.outcome.status}
                {entry.outcome.message
                  ? ` — ${truncate(entry.outcome.message, 28)}`
                  : ""}
              </div>
            </div>
          ),
        },
        style: {
          border: `2px solid ${color}`,
          borderRadius: 6,
          padding: "6px 8px",
          background: "#fff",
          width: NODE_W,
          cursor: "default",
        },
      });

      if (i === newFrom) {
        // First new entry: connect from last shared node (fork edge) or nothing.
        if (rev.forked && newFrom > 0) {
          const src = nid(rev.fork_parent!, newFrom - 1);
          edges.push({
            id: `fork:${src}->${id}`,
            source: src,
            target: id,
            markerEnd: { type: MarkerType.ArrowClosed },
            style: { strokeWidth: 1.5, strokeDasharray: "5 3", stroke: "#6a1b9a" },
            label: `retry r${rev.revision}`,
            labelStyle: { fontSize: 10, fill: "#6a1b9a" },
            labelBgStyle: { fill: "#fafafa" },
          });
        }
      } else {
        // Sequential edge within this revision.
        const prevId = nid(rev.revision, entries[i - 1].index);
        edges.push({
          id: `seq:${prevId}->${id}`,
          source: prevId,
          target: id,
          markerEnd: { type: MarkerType.ArrowClosed },
          style: { strokeWidth: 1.5 },
        });
      }
    }
  }

  return { nodes: layout(nodes, edges, "LR", NODE_W, NODE_H), edges };
}

export function CallGraph({ revisions }: { revisions: RevisionView[] }) {
  const { nodes, edges } = useMemo(() => build(revisions), [revisions]);

  if (!revisions || revisions.length === 0 || nodes.length === 0) {
    return (
      <div className="empty" style={{ margin: "auto", paddingTop: 32 }}>
        No tool calls recorded yet.
      </div>
    );
  }

  return (
    <div style={{ height: "100%", minHeight: 360 }}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
