import { useCallback, useMemo } from "react";
import ReactFlow, {
  Background,
  Controls,
  MarkerType,
  type Edge,
  type Node,
} from "reactflow";
import "reactflow/dist/style.css";
import type { JournalEntry } from "../types";
import { layout, statusColor } from "./graph";

const NODE_W = 240;
const NODE_H = 64;

function truncate(s: string, max = 28): string {
  return s.length > max ? s.slice(0, max - 1) + "…" : s;
}

function nid(index: number, revision: number): string {
  return `p${index}r${revision}`;
}

function build(entries: JournalEntry[], interactive: boolean): { nodes: Node[]; edges: Edge[] } {
  if (!entries || entries.length === 0) return { nodes: [], edges: [] };

  const byIndex = new Map<number, JournalEntry[]>();
  for (const e of entries) {
    const group = byIndex.get(e.index) ?? [];
    group.push(e);
    byIndex.set(e.index, group);
  }

  const indices = [...byIndex.keys()].sort((a, b) => a - b);
  const nodes: Node[] = [];
  const edges: Edge[] = [];

  for (const idx of indices) {
    const group = (byIndex.get(idx) ?? []).sort((a, b) => a.revision - b.revision);

    for (const entry of group) {
      const id = nid(idx, entry.revision);
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
                r{entry.revision} · {entry.outcome.status}
                {entry.outcome.message
                  ? ` — ${truncate(entry.outcome.message, 26)}`
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
          cursor: interactive ? "pointer" : "default",
        },
      });

      if (idx > 0) {
        const prevGroup = byIndex.get(idx - 1);
        if (prevGroup) {
          const pred = [...prevGroup]
            .filter((e) => e.revision <= entry.revision)
            .sort((a, b) => b.revision - a.revision)[0];
          if (pred) {
            const src = nid(idx - 1, pred.revision);
            const isFork = pred.revision < entry.revision;
            edges.push({
              id: `${src}->${id}`,
              source: src,
              target: id,
              markerEnd: { type: MarkerType.ArrowClosed },
              style: isFork
                ? { strokeWidth: 1.5, strokeDasharray: "5 3", stroke: "#6a1b9a" }
                : { strokeWidth: 1.5 },
              ...(isFork
                ? {
                    label: `retry r${entry.revision}`,
                    labelStyle: { fontSize: 10, fill: "#6a1b9a" },
                    labelBgStyle: { fill: "#fafafa" },
                  }
                : {}),
            });
          }
        }
      }
    }
  }

  return { nodes: layout(nodes, edges, "LR", NODE_W, NODE_H), edges };
}

export function CallGraph({
  entries,
  onNodeClick,
}: {
  entries: JournalEntry[];
  onNodeClick?: (index: number, revision: number) => void;
}) {
  const { nodes, edges } = useMemo(
    () => build(entries, !!onNodeClick),
    [entries, onNodeClick],
  );

  const handleNodeClick = useCallback(
    (_: React.MouseEvent, node: Node) => {
      const m = /^p(\d+)r(\d+)$/.exec(node.id);
      if (m) onNodeClick?.(parseInt(m[1], 10), parseInt(m[2], 10));
    },
    [onNodeClick],
  );

  if (!entries || entries.length === 0 || nodes.length === 0) {
    return (
      <div style={{ margin: "auto", paddingTop: 32, color: "var(--text-tertiary)", fontSize: 13, textAlign: "center" }}>
        No tool calls recorded yet.
      </div>
    );
  }

  return (
    <div style={{ height: "100%" }}>
      <ReactFlow
        nodes={nodes}
        edges={edges}
        fitView
        fitViewOptions={{ padding: 0.2 }}
        nodesDraggable={false}
        nodesConnectable={false}
        elementsSelectable={false}
        onNodeClick={onNodeClick ? handleNodeClick : undefined}
      >
        <Background />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
