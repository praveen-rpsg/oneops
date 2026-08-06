import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import Button from '@cloudscape-design/components/button';
import SpaceBetween from '@cloudscape-design/components/space-between';
import type { AssetGraphNode } from '../assetGraph';
import type { LayoutEdge, LayoutNode, TopologyLayout } from '../topologyLayout';
import { NODE_HEIGHT, NODE_WIDTH } from '../topologyLayout';
import type { NodeOverlay, NodeOverlayStatus } from '../topologyOverlay';
import { overlayFor } from '../topologyOverlay';
import type { TopologyPalette } from '../topologyPresentation';

const MIN_SCALE = 0.25;
const MAX_SCALE = 4;
const ZOOM_STEP = 1.25;
const MAX_NAME_CHARS = 22;

function truncate(s: string, max: number): string {
  return s.length > max ? `${s.slice(0, max - 1)}…` : s;
}

function nodeColors(status: NodeOverlayStatus, palette: TopologyPalette, selected: boolean) {
  const base =
    status === 'incident'
      ? { fill: palette.errorFill, stroke: palette.errorStroke }
      : status === 'health'
        ? { fill: palette.warningFill, stroke: palette.warningStroke }
        : { fill: palette.nodeFill, stroke: palette.nodeStroke };
  return {
    fill: base.fill,
    stroke: selected ? palette.selectedStroke : base.stroke,
    strokeWidth: selected ? 3 : 1.5,
  };
}

/** Clips a center-to-center line to the edge of a node's rectangle, so a drawn edge touches the box instead of overlapping its label. */
function clipToBoxEdge(cx: number, cy: number, halfW: number, halfH: number, towardX: number, towardY: number) {
  const dx = towardX - cx;
  const dy = towardY - cy;
  if (dx === 0 && dy === 0) return { x: cx, y: cy };
  const scaleX = dx !== 0 ? halfW / Math.abs(dx) : Number.POSITIVE_INFINITY;
  const scaleY = dy !== 0 ? halfH / Math.abs(dy) : Number.POSITIVE_INFINITY;
  const scale = Math.min(scaleX, scaleY, 1);
  return { x: cx + dx * scale, y: cy + dy * scale };
}

function center(n: LayoutNode) {
  return { x: n.x + NODE_WIDTH / 2, y: n.y + NODE_HEIGHT / 2 };
}

export interface TopologyGraphProps {
  layout: TopologyLayout;
  overlay: Map<string, NodeOverlay>;
  palette: TopologyPalette;
  selectedAssetId?: string;
  onSelectNode: (node: AssetGraphNode) => void;
}

/**
 * The whole topology map's rendering: a hand-rolled, self-contained SVG (no
 * viz library — ADR-NOC-007). Pan is a background drag; zoom is toolbar
 * buttons plus an optional mouse-wheel handler, both implemented by scaling
 * the `viewBox` rather than any DOM transform library.
 */
export function TopologyGraph({ layout, overlay, palette, selectedAssetId, onSelectNode }: TopologyGraphProps) {
  const svgRef = useRef<SVGSVGElement>(null);
  const [scale, setScale] = useState(1);
  const [pan, setPan] = useState({ x: 0, y: 0 });
  const dragRef = useRef<{ startClientX: number; startClientY: number; startPan: { x: number; y: number } } | null>(
    null,
  );

  const viewWidth = layout.width / scale;
  const viewHeight = layout.height / scale;

  const zoomBy = useCallback((factor: number) => {
    setScale((s) => Math.min(MAX_SCALE, Math.max(MIN_SCALE, s * factor)));
  }, []);

  const resetView = useCallback(() => {
    setScale(1);
    setPan({ x: 0, y: 0 });
  }, []);

  // Wheel zoom is attached as a native, non-passive listener: React's own
  // onWheel is passive by default, and preventDefault (needed so the page
  // behind the map does not scroll while zooming) throws in a passive
  // listener.
  useEffect(() => {
    const el = svgRef.current;
    if (!el) return;
    const handler = (e: WheelEvent) => {
      e.preventDefault();
      zoomBy(e.deltaY < 0 ? ZOOM_STEP : 1 / ZOOM_STEP);
    };
    el.addEventListener('wheel', handler, { passive: false });
    return () => el.removeEventListener('wheel', handler);
  }, [zoomBy]);

  const onBackgroundMouseDown = useCallback(
    (e: React.MouseEvent<SVGSVGElement>) => {
      dragRef.current = { startClientX: e.clientX, startClientY: e.clientY, startPan: pan };
    },
    [pan],
  );

  const onBackgroundMouseMove = useCallback(
    (e: React.MouseEvent<SVGSVGElement>) => {
      const drag = dragRef.current;
      const rect = svgRef.current?.getBoundingClientRect();
      if (!drag || !rect || rect.width === 0 || rect.height === 0) return;
      const dxUnits = ((e.clientX - drag.startClientX) / rect.width) * viewWidth;
      const dyUnits = ((e.clientY - drag.startClientY) / rect.height) * viewHeight;
      setPan({ x: drag.startPan.x - dxUnits, y: drag.startPan.y - dyUnits });
    },
    [viewWidth, viewHeight],
  );

  const endDrag = useCallback(() => {
    dragRef.current = null;
  }, []);

  const arrowMarkers = useMemo(
    () => (
      <defs>
        <marker id="topology-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
          <path d="M0,0 L10,5 L0,10 z" fill={palette.edgeStroke} />
        </marker>
        <marker
          id="topology-arrow-back"
          viewBox="0 0 10 10"
          refX="9"
          refY="5"
          markerWidth="7"
          markerHeight="7"
          orient="auto-start-reverse"
        >
          <path d="M0,0 L10,5 L0,10 z" fill={palette.backEdgeStroke} />
        </marker>
      </defs>
    ),
    [palette.edgeStroke, palette.backEdgeStroke],
  );

  return (
    <div>
      <SpaceBetween direction="horizontal" size="xs">
        <Button iconName="zoom-in" variant="icon" ariaLabel="Zoom in" onClick={() => zoomBy(ZOOM_STEP)} />
        <Button iconName="zoom-out" variant="icon" ariaLabel="Zoom out" onClick={() => zoomBy(1 / ZOOM_STEP)} />
        <Button iconName="zoom-to-fit" variant="icon" ariaLabel="Reset view" onClick={resetView} />
      </SpaceBetween>
      <div style={{ marginTop: 8, height: 560, border: `1px solid ${palette.nodeStroke}`, borderRadius: 8, overflow: 'hidden' }}>
        <svg
          ref={svgRef}
          role="img"
          aria-label="Asset dependency topology map"
          width="100%"
          height="100%"
          viewBox={`${pan.x} ${pan.y} ${viewWidth} ${viewHeight}`}
          style={{ background: palette.background, cursor: 'grab', display: 'block' }}
          onMouseDown={onBackgroundMouseDown}
          onMouseMove={onBackgroundMouseMove}
          onMouseUp={endDrag}
          onMouseLeave={endDrag}
        >
          {arrowMarkers}
          <g>
            {layout.edges.map((e: LayoutEdge) => {
              const from = center(e.from);
              const to = center(e.to);
              const start = clipToBoxEdge(from.x, from.y, NODE_WIDTH / 2, NODE_HEIGHT / 2, to.x, to.y);
              const end = clipToBoxEdge(to.x, to.y, NODE_WIDTH / 2, NODE_HEIGHT / 2, from.x, from.y);
              const stroke = e.isBackEdge ? palette.backEdgeStroke : palette.edgeStroke;
              return (
                <line
                  key={`${e.edge.from_asset_id}-${e.edge.to_asset_id}-${e.edge.type}`}
                  x1={start.x}
                  y1={start.y}
                  x2={end.x}
                  y2={end.y}
                  stroke={stroke}
                  strokeWidth={1.5}
                  strokeDasharray={e.isBackEdge ? '4 3' : undefined}
                  markerEnd={`url(#${e.isBackEdge ? 'topology-arrow-back' : 'topology-arrow'})`}
                >
                  <title>
                    {e.edge.from_asset_id} {e.edge.type} {e.edge.to_asset_id}
                    {e.isBackEdge ? ' (cycle)' : ''}
                  </title>
                </line>
              );
            })}
          </g>
          <g>
            {layout.nodes.map((n) => {
              const nodeOverlay = overlayFor(overlay, n.asset.asset_id);
              const selected = n.asset.asset_id === selectedAssetId;
              const colors = nodeColors(nodeOverlay.status, palette, selected);
              return (
                <g
                  key={n.asset.asset_id}
                  transform={`translate(${n.x},${n.y})`}
                  onMouseDown={(e) => e.stopPropagation()}
                  onClick={(e) => {
                    e.stopPropagation();
                    onSelectNode(n.asset);
                  }}
                  style={{ cursor: 'pointer' }}
                >
                  <title>
                    {n.asset.name} ({n.asset.type})
                  </title>
                  <rect
                    width={NODE_WIDTH}
                    height={NODE_HEIGHT}
                    rx={8}
                    ry={8}
                    fill={colors.fill}
                    stroke={colors.stroke}
                    strokeWidth={colors.strokeWidth}
                  />
                  {nodeOverlay.status === 'incident' && (
                    <circle cx={NODE_WIDTH - 12} cy={12} r={5} fill={palette.errorStroke} />
                  )}
                  {nodeOverlay.status === 'health' && (
                    <circle cx={NODE_WIDTH - 12} cy={12} r={5} fill={palette.warningStroke} />
                  )}
                  <text x={12} y={24} fontSize={13} fontWeight={600} fill={palette.nodeText}>
                    {truncate(n.asset.name, MAX_NAME_CHARS)}
                  </text>
                  <text x={12} y={42} fontSize={11} fill={palette.nodeSecondaryText}>
                    {truncate(n.asset.type, MAX_NAME_CHARS)}
                  </text>
                </g>
              );
            })}
          </g>
        </svg>
      </div>
    </div>
  );
}
