<script setup lang="ts">
import { ref, onMounted, onUnmounted, nextTick, computed } from "vue";
import { MessagePlugin } from "tdesign-vue-next";
import {
  getNeo4jGraph,
  searchNeo4jNodes,
  getNeo4jNodeDetail,
  type Neo4jGraphData,
  type Neo4jGraphNode,
  type Neo4jNodeDetail,
} from "@/api/wiki";

// ===== Props =====
const props = defineProps<{
  knowledgeBaseId: string;
}>();

// ===== Constants =====
const GRAPH_LIMIT = 800;        // 默认加载 800 节点（尽量展示完整图谱）
const MAX_REPULSION_DIST = 400;  // 更大的斥力范围，节点更分散
const EDGE_TARGET_DIST = 120;   // 边更长，布局更舒展
const SPRING_STRENGTH = 0.006;  // 弹簧更弱，斥力主导
const ALPHA_DECAY = 0.995;      // 冷却更慢，布局更充分（叶子节点多需要更多迭代展开）
const MIN_ALPHA = 0.01;
const VELOCITY_DAMPING = 0.62;
const MAX_VELOCITY = 20;

// 节点颜色分层（按度数百分比）— 参考 Wiki 图谱的扁平配色风格
const NODE_COLOR_TIERS = [
  { threshold: 0.95, color: "#d54941", label: "核心节点" },  // 红（对比色，同 Wiki comparison）
  { threshold: 0.80, color: "#e37318", label: "重要节点" },  // 橙（同 Wiki concept）
  { threshold: 0.55, color: "#722ed1", label: "一般节点" },  // 紫
  { threshold: 0.30, color: "#0594fa", label: "常见节点" },  // 蓝（同 Wiki synthesis）
  { threshold: 0,    color: "#2ba471", label: "边缘节点" },  // 绿（同 Wiki entity）
];

const EDGE_COLOR = "#c0c4cc";
const EDGE_HIGHLIGHT_COLOR = "#722ed1";

// ===== Reactive state =====
const graphData = ref<Neo4jGraphData | null>(null);
const graphLoading = ref(false);
const graphReady = ref(false);

// 搜索 + 层级控制
const searchValue = ref("");
const searchOptions = ref<Neo4jGraphNode[]>([]);
const searchLoading = ref(false);
const hopCount = ref<1 | 2 | 3>(2);      // 搜索时显示几跳邻居
const hasActiveFilter = ref(false);     // 是否有激活的搜索过滤（精确选中节点后的 N 跳模式）
const activeCenterNode = ref("");       // 当前搜索的中心节点
const hasFuzzyFilter = ref(false);     // 是否有模糊匹配过滤（输入关键词但未精确选中）
const fuzzyMatchCount = ref(0);       // 模糊匹配到的节点数
const showAllLabels = ref(false);     // 是否显示所有节点标签
const isEgoMode = ref(false);         // 是否为 ego 模式（N 跳子图），此模式下所有节点/边都清晰显示

const graphRef = ref<HTMLDivElement | null>(null);
const detailDrawerVisible = ref(false);
const detailNode = ref<Neo4jNodeDetail | null>(null);
const detailLoading = ref(false);

// ===== Module-level simulation state =====
interface GNode {
  x: number;
  y: number;
  vx: number;
  vy: number;
  name: string;
  degree: number;
  pinned: boolean;
  color: string;
  tier: number;
  visible: boolean;       // 当前是否可见（受搜索过滤影响）
}

let graphNodes: GNode[] = [];
let graphEdges: { source: string; target: string; type: string }[] = [];
let graphSvg: SVGSVGElement | null = null;
let graphRootGroup: SVGGElement | null = null;
let graphEdgeGroup: SVGGElement | null = null;
let graphEdgeLabelGroup: SVGGElement | null = null;
let graphNodeGroup: SVGGElement | null = null;
let graphAnimFrame = 0;
let graphAlpha = 1;
let maxDegreeInData = 1;

let svgWidth = 0;
let svgHeight = 0;
let centerX = 0;
let centerY = 0;

let scale = 1;
let translateX = 0;
let translateY = 0;

let isPanning = false;
let panStartX = 0;
let panStartY = 0;
let panStartTX = 0;
let panStartTY = 0;
let dragNode: GNode | null = null;
let dragOffsetX = 0;
let dragOffsetY = 0;

let selectedName: string | null = null;
let hoveredName: string | null = null;
let clickTimer: ReturnType<typeof setTimeout> | null = null;
let hoverLeaveTimer: ReturnType<typeof setTimeout> | null = null;

let mouseDownNode: GNode | null = null;
let mouseDownNodeX = 0;
let mouseDownNodeY = 0;

const nameToNode = new Map<string, GNode>();
const adjacency = new Map<string, Set<string>>();
const nameToEl = new Map<string, SVGGElement>();
const edgeEls: SVGLineElement[] = [];
const edgeLabelEls: SVGTextElement[] = [];

// ===== Computed =====
const statsText = computed(() => {
  if (!graphData.value) return "";
  const visibleCount = graphNodes.filter(n => n.visible).length;
  const total = graphData.value.meta.total_nodes;
  const returned = graphData.value.meta.returned;
  const validCount = graphNodes.length;
  const filteredOut = returned - validCount;
  if (hasActiveFilter.value) {
    return `${visibleCount} 节点 · ${countVisibleEdges()} 关系（共 ${returned}/${total}，已滤除 ${filteredOut} 个空壳节点）`;
  }
  if (filteredOut > 0) {
    return `${validCount} / ${total} 节点 · ${graphEdges.length} 关系（已滤除 ${filteredOut} 个空壳节点）`;
  }
  return `${returned} / ${total} 节点 · ${graphEdges.length} 关系`;
});

function countVisibleEdges(): number {
  let count = 0;
  for (const e of edgeEls) {
    if (e.style.display !== "none") count++;
  }
  return count;
}

// ===== API: Load full graph =====
async function loadFullGraph() {
  graphLoading.value = true;
  graphReady.value = false;
  isEgoMode.value = false;
  try {
    const res: any = await getNeo4jGraph(props.knowledgeBaseId, {
      mode: "overview",
      limit: GRAPH_LIMIT,
    });
    const data = res?.data || res;
    graphData.value = data;
    initGraphFromData(data);
    await nextTick();
    renderGraph();
    startSimulation();
  } catch (e: any) {
    MessagePlugin.error(e?.message || "加载图谱失败");
  } finally {
    graphLoading.value = false;
    graphReady.value = true;
  }
}

// ===== 节点颜色分层 =====
function getNodeTierInfo(degree: number): { color: string; tier: number } {
  if (maxDegreeInData <= 0) {
    return { color: NODE_COLOR_TIERS[2].color, tier: 2 };
  }
  const pct = degree / maxDegreeInData;
  for (let i = 0; i < NODE_COLOR_TIERS.length; i++) {
    if (pct >= NODE_COLOR_TIERS[i].threshold) {
      return { color: NODE_COLOR_TIERS[i].color, tier: i };
    }
  }
  const last = NODE_COLOR_TIERS.length - 1;
  return { color: NODE_COLOR_TIERS[last].color, tier: last };
}

// ===== 节点半径（与 Wiki 图谱一致：log 缩放，8~24px）=====
function nodeRadius(n: GNode): number {
  return Math.max(8, Math.min(24, 8 + Math.log(n.degree + 1) * 4));
}

// ===== 初始化图数据 =====
function initGraphFromData(data: Neo4jGraphData) {
  graphNodes = [];
  graphEdges = [];
  nameToNode.clear();
  adjacency.clear();

  // 过滤空壳节点：chunk_count=0 且 attributes 为空的节点
  // （这类节点是导入关系时 apoc.merge.node 自动创建的占位节点，不是真正提取的实体）
  const emptyNodes = data.nodes.filter(node => {
    const hasChunks = (node.chunk_count || 0) > 0;
    const hasAttrs = (node.attributes || []).length > 0;
    const hasName = node.name && node.name.trim().length > 0;
    return !hasName || (!hasChunks && !hasAttrs);
  });
  if (emptyNodes.length > 0) {
    console.log(`[Neo4jGraph] 滤除 ${emptyNodes.length} 个空壳节点：`, emptyNodes.map(n => `${n.name} (degree=${n.degree}, chunks=${n.chunk_count}, attrs=${n.attributes?.length || 0})`));
  }
  const validNodes = data.nodes.filter(node => !emptyNodes.includes(node));

  const count = validNodes.length;
  maxDegreeInData = validNodes.reduce((m, n) => Math.max(m, n.degree), 1);

  // 按度数降序排列，先放大节点
  const sortedNodes = [...validNodes].sort((a, b) => b.degree - a.degree);

  sortedNodes.forEach((node, i) => {
    const { color, tier } = getNodeTierInfo(node.degree);
    const angle = (2 * Math.PI * i) / Math.max(count, 1);
    const radiusRatio = tier === 0 ? 0.15 : tier === 1 ? 0.35 : tier === 2 ? 0.55 : tier === 3 ? 0.75 : 1.0;
    const r = Math.min(svgWidth, svgHeight) * 0.38 * radiusRatio + (Math.random() - 0.5) * 40;

    const gNode: GNode = {
      x: centerX + r * Math.cos(angle),
      y: centerY + r * Math.sin(angle),
      vx: 0,
      vy: 0,
      name: node.name,
      degree: node.degree,
      pinned: false,
      color,
      tier,
      visible: true,
    };
    graphNodes.push(gNode);
    nameToNode.set(node.name, gNode);
    adjacency.set(node.name, new Set());
  });

  // 只保留两端都在有效节点集中的边
  data.relations.forEach((rel) => {
    const srcExists = nameToNode.has(rel.source);
    const tgtExists = nameToNode.has(rel.target);
    if (!srcExists || !tgtExists) return;
    if (rel.source === rel.target) return; // 跳过自环
    graphEdges.push({ source: rel.source, target: rel.target, type: rel.type });
    adjacency.get(rel.source)?.add(rel.target);
    adjacency.get(rel.target)?.add(rel.source);
  });
}

// ===== 渲染图谱 =====
function renderGraph() {
  if (!graphSvg || !graphRef.value) return;

  const rect = graphRef.value.getBoundingClientRect();
  svgWidth = rect.width;
  svgHeight = rect.height;
  centerX = svgWidth / 2;
  centerY = svgHeight / 2;

  graphSvg.setAttribute("viewBox", `0 0 ${svgWidth} ${svgHeight}`);
  graphSvg.setAttribute("width", "100%");
  graphSvg.setAttribute("height", "100%");

  if (graphRootGroup) graphRootGroup.remove();
  edgeEls.length = 0;
  edgeLabelEls.length = 0;
  nameToEl.clear();

  const NS = "http://www.w3.org/2000/svg";

  graphRootGroup = document.createElementNS(NS, "g");
  graphRootGroup.setAttribute("class", "graph-root");
  graphSvg.appendChild(graphRootGroup);
  updatePanZoomTransform();

  graphEdgeGroup = document.createElementNS(NS, "g");
  graphEdgeGroup.setAttribute("class", "graph-edges");
  graphRootGroup.appendChild(graphEdgeGroup);

  graphEdgeLabelGroup = document.createElementNS(NS, "g");
  graphEdgeLabelGroup.setAttribute("class", "graph-edge-labels");
  graphRootGroup.appendChild(graphEdgeLabelGroup);

  graphNodeGroup = document.createElementNS(NS, "g");
  graphNodeGroup.setAttribute("class", "graph-nodes");
  graphRootGroup.appendChild(graphNodeGroup);

  // 检测双向边（A→B 且 B→A 都存在）
  const edgeDirSet = new Set<string>();
  for (const edge of graphEdges) {
    edgeDirSet.add(`${edge.source}→${edge.target}`);
  }

  // 创建边（双向边合并为一条线，两端都有箭头；单向线目标端有箭头）
  const processedPairs = new Set<string>();
  for (const edge of graphEdges) {
    const pairKey = [edge.source, edge.target].sort().join("↔");
    if (processedPairs.has(pairKey)) continue;
    processedPairs.add(pairKey);

    const bidir = edgeDirSet.has(`${edge.target}→${edge.source}`);
    const src = nameToNode.get(edge.source);
    const tgt = nameToNode.get(edge.target);

    // Wiki 图谱风格：统一 1.2px / 0.4 透明度，更清爽
    const baseWidth = 1.2;
    const baseOpacity = 0.4;

    const line = document.createElementNS(NS, "line");
    line.setAttribute("class", "graph-edge");
    line.setAttribute("stroke", EDGE_COLOR);
    line.setAttribute("stroke-width", String(baseWidth));
    line.setAttribute("stroke-opacity", String(baseOpacity));
    line.setAttribute("data-base-width", String(baseWidth));
    line.setAttribute("data-base-opacity", String(baseOpacity));
    line.setAttribute("marker-end", "url(#arrow-end)");
    if (bidir) line.setAttribute("marker-start", "url(#arrow-start)");
    line.dataset.source = edge.source;
    line.dataset.target = edge.target;
    line.dataset.type = edge.type;
    line.dataset.bidir = bidir ? "1" : "0";
    line.style.transition = "stroke 0.2s, stroke-width 0.2s, stroke-opacity 0.2s";
    graphEdgeGroup.appendChild(line);
    edgeEls.push(line);

    // 关系类型标签（默认隐藏，hover 或高亮时显示）
    const edgeLabelBg = document.createElementNS(NS, "rect");
    edgeLabelBg.setAttribute("rx", "3");
    edgeLabelBg.setAttribute("ry", "3");
    edgeLabelBg.setAttribute("fill", "#fff");
    edgeLabelBg.setAttribute("opacity", "0.85");
    edgeLabelBg.style.pointerEvents = "none";

    const edgeLabel = document.createElementNS(NS, "text");
    edgeLabel.setAttribute("text-anchor", "middle");
    edgeLabel.setAttribute("font-size", "10");
    edgeLabel.setAttribute("fill", "#888");
    edgeLabel.style.pointerEvents = "none";
    edgeLabel.style.opacity = "0";
    edgeLabel.style.transition = "opacity 0.2s";
    edgeLabel.textContent = edge.type;
    edgeLabel.classList.add("edge-label");
    edgeLabel.dataset.source = edge.source;
    edgeLabel.dataset.target = edge.target;
    edgeLabel.dataset.bidir = bidir ? "1" : "0";

    const labelGroup = document.createElementNS(NS, "g");
    labelGroup.style.pointerEvents = "none";
    labelGroup.appendChild(edgeLabelBg);
    labelGroup.appendChild(edgeLabel);
    graphEdgeLabelGroup.appendChild(labelGroup);
    edgeLabelEls.push(edgeLabel);
  }

  // 创建节点（Wiki 图谱风格：扁平纯色圆 + 文字 text-shadow）
  for (const node of graphNodes) {
    const g = document.createElementNS(NS, "g");
    g.setAttribute("class", "graph-node");
    g.setAttribute("transform", `translate(${node.x}, ${node.y})`);
    g.dataset.name = node.name;
    g.style.cursor = "pointer";

    const r = nodeRadius(node);

    // 扩展虚线环（隐藏邻居指示，hover 显示）
    const expansionRing = document.createElementNS(NS, "circle");
    expansionRing.setAttribute("r", String(r + 3));
    expansionRing.setAttribute("fill", "none");
    expansionRing.setAttribute("stroke", node.color);
    expansionRing.setAttribute("stroke-width", "1.5");
    expansionRing.setAttribute("stroke-dasharray", "3 3");
    expansionRing.setAttribute("pointer-events", "none");
    expansionRing.style.opacity = "0";
    expansionRing.style.transition = "opacity 0.2s";
    expansionRing.classList.add("node-expansion-ring");
    g.appendChild(expansionRing);

    // 激活环（选中状态）
    const activeRing = document.createElementNS(NS, "circle");
    activeRing.setAttribute("r", String(r + 5));
    activeRing.setAttribute("fill", "none");
    activeRing.setAttribute("stroke", node.color);
    activeRing.setAttribute("stroke-width", "2");
    activeRing.setAttribute("pointer-events", "none");
    activeRing.style.opacity = "0";
    activeRing.style.transition = "opacity 0.2s";
    activeRing.classList.add("node-active-ring");
    g.appendChild(activeRing);

    // 主圆（扁平纯色 + 白边）
    const circle = document.createElementNS(NS, "circle");
    circle.setAttribute("r", String(r));
    circle.setAttribute("fill", node.color);
    circle.setAttribute("stroke", "#fff");
    circle.setAttribute("stroke-width", "2");
    circle.style.transition = "r 0.2s, stroke-width 0.2s, opacity 0.2s";
    circle.classList.add("node-circle");
    g.appendChild(circle);

    // 标签文字（text-shadow 实现描边效果，与 Wiki 图谱一致）
    const text = document.createElementNS(NS, "text");
    text.setAttribute("text-anchor", "middle");
    text.setAttribute("dy", String(r + 14));
    text.setAttribute("font-size", "11");
    text.setAttribute("fill", "var(--td-text-color-secondary)");
    text.setAttribute("pointer-events", "none");
    text.style.transition = "opacity 0.2s";
    text.style.opacity = "0";
    text.style.textShadow =
      "0 1px 3px var(--td-bg-color-container), 0 -1px 3px var(--td-bg-color-container), " +
      "1px 0 3px var(--td-bg-color-container), -1px 0 3px var(--td-bg-color-container)";
    text.textContent = truncateText(node.name, 14);
    text.classList.add("node-label");
    g.appendChild(text);

    g.addEventListener("mousedown", (e) => onNodeMouseDown(e, node));
    g.addEventListener("mouseenter", () => onNodeHover(node.name));
    g.addEventListener("mouseleave", () => onNodeLeave());
    g.addEventListener("dblclick", (e) => onNodeDoubleClick(e, node));

    graphNodeGroup.appendChild(g);
    nameToEl.set(node.name, g);
  }
}

function truncateText(text: string, maxLen: number): string {
  return text.length > maxLen ? text.slice(0, maxLen - 1) + "…" : text;
}

// ===== 标签显隐（Wiki 图谱风格：text-shadow 描边，无背景框）=====
function showNodeLabel(el: SVGGElement, show: boolean) {
  const label = el.querySelector(".node-label") as SVGTextElement | null;
  const expansion = el.querySelector(".node-expansion-ring") as SVGCircleElement | null;
  if (label) label.style.opacity = show ? "1" : "0";
  if (expansion) expansion.style.opacity = show ? "0.55" : "0";
}

function showAllLabelsToggle() {
  showAllLabels.value = !showAllLabels.value;
  updateLabelsVisibility();
}

function updateLabelsVisibility() {
  for (const [name, el] of nameToEl) {
    const node = nameToNode.get(name);
    if (!node || !node.visible) continue;
    // 手动开启全部标签，或 tier ≤ 2，或叶子节点（degree=1，避免边指向"空"点的错觉）
    const shouldShow = showAllLabels.value || node.tier <= 2 || node.degree <= 1;
    showNodeLabel(el, shouldShow);
  }
}

// ===== Force Simulation =====
let labelsShown = false;

function startSimulation() {
  graphAlpha = 1;
  labelsShown = false;
  cancelAnimationFrame(graphAnimFrame);
  tick();
}

function tick() {
  if (graphAlpha < MIN_ALPHA) {
    graphAnimFrame = 0;
    if (!labelsShown) {
      labelsShown = true;
      updateLabelsVisibility();
    }
    return;
  }
  labelsShown = false;

  // 1. Repulsion + collision
  const sorted = [...graphNodes].filter(n => n.visible).sort((a, b) => a.x - b.x);
  for (let i = 0; i < sorted.length; i++) {
    const a = sorted[i];
    const ra = nodeRadius(a);
    // 叶子节点斥力加成：degree 越低斥力越大，防止扎堆
    const repulseA = a.degree <= 1 ? 1.8 : a.degree <= 2 ? 1.3 : 1.0;
    for (let j = i + 1; j < sorted.length; j++) {
      const b = sorted[j];
      const dx = b.x - a.x;
      if (dx > MAX_REPULSION_DIST) break;
      const dy = b.y - a.y;
      if (Math.abs(dy) > MAX_REPULSION_DIST) continue;
      const distSq = dx * dx + dy * dy;
      if (distSq < 1) continue;
      const dist = Math.sqrt(distSq);
      const rb = nodeRadius(b);
      const repulseB = b.degree <= 1 ? 1.8 : b.degree <= 2 ? 1.3 : 1.0;
      const repulseMul = repulseA * repulseB;

      // 基础斥力
      const force = (220 * graphAlpha * repulseMul) / distSq * 60 / dist;
      let fx = dx * force;
      let fy = dy * force;

      // 碰撞约束：距离小于最小间距（半径之和 + 6px 缓冲）时强力推开
      const minDist = ra + rb + 6;
      if (dist < minDist) {
        const pushStrength = (minDist - dist) * 0.6 * graphAlpha;
        fx += (dx / dist) * pushStrength;
        fy += (dy / dist) * pushStrength;
      }

      if (!a.pinned) { a.vx -= fx; a.vy -= fy; }
      if (!b.pinned) { b.vx += fx; b.vy += fy; }
    }
  }

  // 2. Spring attraction（叶子节点的边更长，让它往外圈跑）
  for (const edge of graphEdges) {
    const src = nameToNode.get(edge.source);
    const tgt = nameToNode.get(edge.target);
    if (!src || !tgt || !src.visible || !tgt.visible) continue;
    const dx = tgt.x - src.x;
    const dy = tgt.y - src.y;
    const dist = Math.sqrt(dx * dx + dy * dy) || 0.1;
    // 叶子节点（degree=1）的目标边距增大 60%，防止挤在中心附近
    const isLeafEdge = src.degree <= 1 || tgt.degree <= 1;
    const targetDist = isLeafEdge ? EDGE_TARGET_DIST * 1.6 : EDGE_TARGET_DIST;
    const springStrength = isLeafEdge ? SPRING_STRENGTH * 0.7 : SPRING_STRENGTH;
    const displacement = (dist - targetDist) * springStrength * graphAlpha;
    const fx = (dx / dist) * displacement;
    const fy = (dy / dist) * displacement;
    if (!src.pinned) { src.vx += fx; src.vy += fy; }
    if (!tgt.pinned) { tgt.vx -= fx; tgt.vy -= fy; }
  }

  // 3. Center gravity
  const gravity = Math.min(0.008, 0.0008 + graphNodes.filter(n => n.visible).length * 0.000015) * graphAlpha;
  for (const node of graphNodes) {
    if (node.pinned || !node.visible) continue;
    node.vx += (centerX - node.x) * gravity;
    node.vy += (centerY - node.y) * gravity;
  }

  // 4. Integration
  for (const node of graphNodes) {
    if (node.pinned || !node.visible) continue;
    node.vx *= VELOCITY_DAMPING;
    node.vy *= VELOCITY_DAMPING;
    const speed = Math.sqrt(node.vx * node.vx + node.vy * node.vy);
    if (speed > MAX_VELOCITY) {
      node.vx = (node.vx / speed) * MAX_VELOCITY;
      node.vy = (node.vy / speed) * MAX_VELOCITY;
    }
    node.x += node.vx;
    node.y += node.vy;
  }

  updateNodePositions();
  updateEdgePositions();

  if (graphAlpha < 0.3 && !labelsShown) {
    labelsShown = true;
    showAllLabels(true);
  }

  graphAlpha *= ALPHA_DECAY;
  graphAnimFrame = requestAnimationFrame(tick);
}

function updateNodePositions() {
  for (const node of graphNodes) {
    const el = nameToEl.get(node.name);
    if (el) el.setAttribute("transform", `translate(${node.x}, ${node.y})`);
  }
}

function updateEdgePositions() {
  for (const line of edgeEls) {
    const srcName = line.dataset.source!;
    const tgtName = line.dataset.target!;
    const src = nameToNode.get(srcName);
    const tgt = nameToNode.get(tgtName);
    if (!src || !tgt) continue;

    const dx = tgt.x - src.x;
    const dy = tgt.y - src.y;
    const dist = Math.sqrt(dx * dx + dy * dy) || 0.1;
    const ux = dx / dist;
    const uy = dy / dist;

    // Wiki 图谱风格：缩短 r + 4，给箭头留余量
    const rS = nodeRadius(src) + 4;
    const rT = nodeRadius(tgt) + 4;

    line.setAttribute("x1", String(src.x + ux * rS));
    line.setAttribute("y1", String(src.y + uy * rS));
    line.setAttribute("x2", String(tgt.x - ux * rT));
    line.setAttribute("y2", String(tgt.y - uy * rT));
  }

  // 更新边标签位置
  for (const label of edgeLabelEls) {
    const srcName = label.dataset.source!;
    const tgtName = label.dataset.target!;
    const src = nameToNode.get(srcName);
    const tgt = nameToNode.get(tgtName);
    if (!src || !tgt) continue;

    const mx = (src.x + tgt.x) / 2;
    const my = (src.y + tgt.y) / 2;

    // 让标签沿边的方向有一点偏移，避免正好压在线上
    const dx = tgt.x - src.x;
    const dy = tgt.y - src.y;
    const dist = Math.sqrt(dx * dx + dy * dy) || 0.1;
    const nx = -dy / dist;
    const ny = dx / dist;
    const offset = 6;

    const lx = mx + nx * offset;
    const ly = my + ny * offset;

    label.setAttribute("x", String(lx));
    label.setAttribute("y", String(ly));

    // 更新背景矩形位置
    const bg = label.previousElementSibling as SVGRectElement | null;
    if (bg) {
      const bbox = label.getBBox();
      bg.setAttribute("x", String(bbox.x - 3));
      bg.setAttribute("y", String(bbox.y - 1));
      bg.setAttribute("width", String(bbox.width + 6));
      bg.setAttribute("height", String(bbox.height + 2));
    }
  }
}

// ===== Pan & Zoom =====
function updatePanZoomTransform() {
  if (!graphRootGroup) return;
  graphRootGroup.setAttribute(
    "transform",
    `translate(${translateX}, ${translateY}) scale(${scale})`
  );
}

function onWheel(e: WheelEvent) {
  e.preventDefault();
  const rect = graphSvg!.getBoundingClientRect();
  const cx = e.clientX - rect.left;
  const cy = e.clientY - rect.top;
  const factor = e.deltaY > 0 ? 0.9 : 1.1;
  const newScale = Math.max(0.25, Math.min(6, scale * factor));
  if (newScale === scale) return;
  translateX = cx - (cx - translateX) * (newScale / scale);
  translateY = cy - (cy - translateY) * (newScale / scale);
  scale = newScale;
  updatePanZoomTransform();
}

function onSvgMouseDown(e: MouseEvent) {
  if ((e.target as Element).closest(".graph-node")) return;
  isPanning = true;
  panStartX = e.clientX;
  panStartY = e.clientY;
  panStartTX = translateX;
  panStartTY = translateY;
  graphSvg!.style.cursor = "grabbing";
}

function onWindowMouseMove(e: MouseEvent) {
  if (isPanning) {
    translateX = panStartTX + (e.clientX - panStartX);
    translateY = panStartTY + (e.clientY - panStartY);
    updatePanZoomTransform();
  }
  if (dragNode) {
    const pt = screenToSvg(e.clientX, e.clientY);
    dragNode.x = pt.x - dragOffsetX;
    dragNode.y = pt.y - dragOffsetY;
    dragNode.vx = 0;
    dragNode.vy = 0;
    const el = nameToEl.get(dragNode.name);
    if (el) el.setAttribute("transform", `translate(${dragNode.x}, ${dragNode.y})`);
    updateEdgePositions();
  }
}

function onWindowMouseUp(e: MouseEvent) {
  const wasDraggingNode = dragNode !== null;
  const clickedNode = mouseDownNode;
  const clickedX = mouseDownNodeX;
  const clickedY = mouseDownNodeY;

  if (isPanning) {
    isPanning = false;
    graphSvg!.style.cursor = "default";
    const dx = Math.abs(e.clientX - panStartX);
    const dy = Math.abs(e.clientY - panStartY);
    if (dx < 5 && dy < 5) clearSelection();
  }

  dragNode = null;

  if (wasDraggingNode && clickedNode) {
    const moved = Math.sqrt((clickedNode.x - clickedX) ** 2 + (clickedNode.y - clickedY) ** 2);
    if (moved < 3) handleNodeClick(clickedNode.name);
  }
  mouseDownNode = null;
}

function screenToSvg(clientX: number, clientY: number) {
  if (!graphSvg) return { x: 0, y: 0 };
  const rect = graphSvg.getBoundingClientRect();
  return {
    x: (clientX - rect.left - translateX) / scale,
    y: (clientY - rect.top - translateY) / scale,
  };
}

// ===== Node interactions =====
function onNodeMouseDown(e: MouseEvent, node: GNode) {
  e.stopPropagation();
  if (!node.visible) return;
  mouseDownNode = node;
  mouseDownNodeX = node.x;
  mouseDownNodeY = node.y;
  dragNode = node;
  node.pinned = true;
  const pt = screenToSvg(e.clientX, e.clientY);
  dragOffsetX = pt.x - node.x;
  dragOffsetY = pt.y - node.y;
}

function onNodeHover(name: string) {
  if (hoverLeaveTimer) { clearTimeout(hoverLeaveTimer); hoverLeaveTimer = null; }
  hoveredName = name;
  if (selectedName) applyHighlight(selectedName, name);
  else applyHighlight(name);
}

function onNodeLeave() {
  if (hoverLeaveTimer) clearTimeout(hoverLeaveTimer);
  hoverLeaveTimer = setTimeout(() => {
    hoveredName = null;
    if (selectedName) applyHighlight(selectedName);
    else clearHighlight();
  }, 80);
}

function onNodeDoubleClick(e: MouseEvent, node: GNode) {
  e.stopPropagation();
  if (clickTimer) { clearTimeout(clickTimer); clickTimer = null; }
  // 双击切换为该节点的 N 跳视图
  setActiveCenter(node.name);
}

function handleNodeClick(name: string) {
  if (clickTimer) clearTimeout(clickTimer);
  clickTimer = setTimeout(() => {
    selectedName = name;
    applyHighlight(name);
    openDetailDrawer(name);
    panToNode(name, -220);
  }, 220);
}

// ===== Highlight（Wiki 图谱风格）=====
function applyHighlight(primaryName: string, secondaryName?: string) {
  const primaryNode = nameToNode.get(primaryName);
  const hlColor = primaryNode?.color || EDGE_HIGHLIGHT_COLOR;

  const primaryNeighbors = adjacency.get(primaryName) || new Set();
  const secondaryNeighbors = secondaryName ? adjacency.get(secondaryName) || new Set() : new Set();
  const allNeighbors = new Set([...primaryNeighbors, ...secondaryNeighbors]);
  allNeighbors.add(primaryName);
  if (secondaryName) allNeighbors.add(secondaryName);

  // ego 模式下不淡化非邻居节点，全部保持清晰
  const dimOtherNodes = !isEgoMode.value;
  const otherNodeOpacity = dimOtherNodes ? "0.2" : "1";
  const otherEdgeOpacity = dimOtherNodes ? "0.08" : "0.85";

  for (const [name, el] of nameToEl) {
    const node = nameToNode.get(name);
    if (!node || !node.visible) continue;
    const circle = el.querySelector(".node-circle") as SVGCircleElement | null;
    const activeRing = el.querySelector(".node-active-ring") as SVGCircleElement | null;
    if (!circle) continue;

    const isPrimary = name === primaryName;
    const isSecondary = name === secondaryName;
    const isNeighbor = allNeighbors.has(name);

    if (isPrimary || isSecondary) {
      // 选中节点：激活环亮起
      if (activeRing) activeRing.style.opacity = "1";
      circle.setAttribute("stroke-width", "2");
      el.style.opacity = "1";
      showNodeLabel(el, true);
    } else if (isNeighbor) {
      // 邻居：完全不透明
      if (activeRing) activeRing.style.opacity = "0";
      circle.setAttribute("stroke-width", "2");
      el.style.opacity = "1";
      showNodeLabel(el, true);
    } else {
      // 非邻居：ego 模式保持清晰，概览模式淡化
      if (activeRing) activeRing.style.opacity = "0";
      circle.setAttribute("stroke-width", "2");
      el.style.opacity = otherNodeOpacity;
      if (dimOtherNodes) showNodeLabel(el, false);
    }
  }

  for (const line of edgeEls) {
    const src = line.dataset.source!;
    const tgt = line.dataset.target!;
    const isBidir = line.dataset.bidir === "1";
    // 与 primary/secondary 直接相连的边高亮
    const isConnected =
      src === primaryName || tgt === primaryName ||
      (secondaryName && (src === secondaryName || tgt === secondaryName));

    if (isConnected) {
      line.setAttribute("stroke", hlColor);
      line.setAttribute("stroke-width", "2");
      line.setAttribute("stroke-opacity", "0.9");
      line.setAttribute("marker-end", "url(#arrow-end-hl)");
      if (isBidir) line.setAttribute("marker-start", "url(#arrow-start-hl)");
    } else {
      line.setAttribute("stroke", EDGE_COLOR);
      line.setAttribute("stroke-width", dimOtherNodes ? "1" : "1.3");
      line.setAttribute("stroke-opacity", otherEdgeOpacity);
      line.setAttribute("marker-end", "url(#arrow-end)");
      if (isBidir) line.setAttribute("marker-start", "url(#arrow-start)");
    }
  }

  // 高亮时显示相连边的关系标签
  for (const label of edgeLabelEls) {
    const src = label.dataset.source!;
    const tgt = label.dataset.target!;
    const isConnected =
      src === primaryName || tgt === primaryName ||
      (secondaryName && (src === secondaryName || tgt === secondaryName));
    label.style.opacity = isConnected ? "1" : "0";
    label.setAttribute("fill", isConnected ? "#722ed1" : "#888");
  }
}

function clearHighlight() {
  // ego 模式下：所有节点和边都清晰可见（不淡化），因为都是 N 跳内的
  // 概览模式下：边用淡色（Wiki 风格）
  const edgeOpacity = isEgoMode.value ? "0.85" : "0.4";
  const edgeWidth = isEgoMode.value ? "1.3" : "1.2";

  for (const [name, el] of nameToEl) {
    const node = nameToNode.get(name);
    if (!node) continue;
    const circle = el.querySelector(".node-circle") as SVGCircleElement | null;
    const activeRing = el.querySelector(".node-active-ring") as SVGCircleElement | null;
    const r = nodeRadius(node);
    if (circle) {
      circle.setAttribute("r", String(r));
      circle.setAttribute("stroke-width", "2");
    }
    if (activeRing) activeRing.style.opacity = "0";
    el.style.opacity = node.visible ? "1" : "0";
  }
  updateLabelsVisibility();
  for (const line of edgeEls) {
    line.setAttribute("stroke", EDGE_COLOR);
    line.setAttribute("stroke-width", edgeWidth);
    line.setAttribute("stroke-opacity", edgeOpacity);
    line.setAttribute("marker-end", "url(#arrow-end)");
    const isBidir = line.dataset.bidir === "1";
    if (isBidir) line.setAttribute("marker-start", "url(#arrow-start)");
    else line.removeAttribute("marker-start");
  }

  // 清除高亮时隐藏边标签
  for (const label of edgeLabelEls) {
    label.style.opacity = "0";
    label.setAttribute("fill", "#888");
  }
}

function clearSelection() {
  selectedName = null;
  detailDrawerVisible.value = false;
  clearHighlight();
}

function panToNode(name: string, offsetX = 0) {
  const node = nameToNode.get(name);
  if (!node) return;
  translateX = svgWidth / 2 + offsetX - node.x * scale;
  translateY = svgHeight / 2 - node.y * scale;
  updatePanZoomTransform();
}

function fitToView() {
  const visible = graphNodes.filter(n => n.visible);
  if (visible.length === 0) return;
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity;
  for (const node of visible) {
    const r = nodeRadius(node);
    minX = Math.min(minX, node.x - r);
    minY = Math.min(minY, node.y - r);
    maxX = Math.max(maxX, node.x + r);
    maxY = Math.max(maxY, node.y + r);
  }
  const graphW = maxX - minX || 100;
  const graphH = maxY - minY || 100;
  const scaleX = svgWidth / graphW * 0.88;
  const scaleY = svgHeight / graphH * 0.88;
  scale = Math.min(scaleX, scaleY, 4);
  translateX = (svgWidth - (minX + maxX) * scale) / 2;
  translateY = (svgHeight - (minY + maxY) * scale) / 2;
  updatePanZoomTransform();
}

// ===== 搜索 + 跳数过滤 =====
let searchDebounceTimer: ReturnType<typeof setTimeout> | null = null;
let searchSeq = 0;

// t-select 的 :on-search 回调：用户输入搜索关键词时触发
// 同时做：1) 前端模糊匹配过滤 2) 后端搜索建议
function onSearchInput(keyword: string) {
  const q = (keyword || "").trim();

  if (searchDebounceTimer) {
    clearTimeout(searchDebounceTimer);
    searchDebounceTimer = null;
  }

  // 清空关键词 → 恢复全图（如果当前是模糊过滤模式）
  if (!q) {
    searchOptions.value = [];
    searchLoading.value = false;
    if (hasFuzzyFilter.value && !hasActiveFilter.value) {
      clearAllFilters();
    }
    return;
  }

  // 有关键词且当前不是精确选中模式 → 立即做前端模糊过滤
  if (!hasActiveFilter.value) {
    applyFuzzyFilter(q.toLowerCase());
  }

  // 防抖请求后端搜索建议
  const seq = ++searchSeq;
  searchDebounceTimer = setTimeout(async () => {
    searchLoading.value = true;
    try {
      const res: any = await searchNeo4jNodes(props.knowledgeBaseId, q, 15);
      if (seq !== searchSeq) return; // 过期响应丢弃
      searchOptions.value = res?.data || res || [];
    } catch {
      if (seq !== searchSeq) return;
      searchOptions.value = [];
    } finally {
      if (seq === searchSeq) searchLoading.value = false;
    }
  }, 200);
}

// 模糊匹配过滤：显示名称包含关键词的节点及其相连的边
function applyFuzzyFilter(keyword: string) {
  hasFuzzyFilter.value = true;
  const kw = keyword.toLowerCase();

  // 找出所有名称匹配的节点
  const matchedNames = new Set<string>();
  for (const node of graphNodes) {
    if (node.name.toLowerCase().includes(kw)) {
      matchedNames.add(node.name);
    }
  }
  fuzzyMatchCount.value = matchedNames.size;

  // 同时显示匹配节点的直接邻居（方便看到上下文）
  const visibleNames = new Set(matchedNames);
  for (const name of matchedNames) {
    const neighbors = adjacency.get(name);
    if (neighbors) {
      for (const nb of neighbors) visibleNames.add(nb);
    }
  }

  // 更新节点可见性
  for (const node of graphNodes) {
    node.visible = visibleNames.has(node.name);
    const el = nameToEl.get(node.name);
    if (el) {
      el.style.display = node.visible ? "" : "none";
      // 匹配到的节点完整显示，邻居节点稍淡
      if (node.visible) {
        el.style.opacity = matchedNames.has(node.name) ? "1" : "0.5";
      }
    }
  }

  // 边：至少一端匹配关键词才显示
  for (const line of edgeEls) {
    const src = line.dataset.source!;
    const tgt = line.dataset.target!;
    const visible = matchedNames.has(src) || matchedNames.has(tgt);
    line.style.display = visible ? "" : "none";
  }

  // 边标签
  for (const label of edgeLabelEls) {
    const src = label.dataset.source!;
    const tgt = label.dataset.target!;
    const visible = matchedNames.has(src) || matchedNames.has(tgt);
    const parent = label.parentElement;
    if (parent) parent.style.display = visible ? "" : "none";
  }

  nextTick(() => {
    fitToView();
  });
}

// 选中搜索建议 → 精确选中节点，加载 N 跳子图
function onSearchSelect(name: string) {
  if (!name) return;
  setActiveCenter(name);
}

function clearFilter() {
  searchValue.value = "";
  // 如果当前是 ego 模式 → 回到全量 overview
  if (hasActiveFilter.value && activeCenterNode.value) {
    hasActiveFilter.value = false;
    hasFuzzyFilter.value = false;
    activeCenterNode.value = "";
    selectedName = null;
    loadFullGraph();
  } else {
    clearAllFilters();
  }
}

function clearAllFilters() {
  hasActiveFilter.value = false;
  hasFuzzyFilter.value = false;
  activeCenterNode.value = "";
  selectedName = null;

  // 显示所有节点和边
  for (const node of graphNodes) {
    node.visible = true;
    const el = nameToEl.get(node.name);
    if (el) {
      el.style.display = "";
      el.style.opacity = "1";
    }
  }
  updateLabelsVisibility();
  for (const line of edgeEls) {
    line.style.display = "";
  }
  for (const label of edgeLabelEls) {
    const parent = label.parentElement;
    if (parent) parent.style.display = "";
    label.style.opacity = "0";
  }

  clearHighlight();
  fitToView();
  startSimulation();
}

// 加载 ego 图（以指定节点为中心的 N 跳邻域子图）
// ego 模式特点：所有节点/边都清晰显示，不淡化（因为都是 N 跳内的有效路径）
async function loadEgoGraph(centerName: string, depth: number) {
  graphLoading.value = true;
  isEgoMode.value = true;
  try {
    const res: any = await getNeo4jGraph(props.knowledgeBaseId, {
      mode: "ego",
      center: centerName,
      depth,
      limit: 500,
    });
    const data = res?.data || res;
    graphData.value = data;
    initGraphFromData(data);
    await nextTick();
    renderGraph();
    startSimulation();

    // 标记中心节点（激活环）+ 打开详情抽屉
    selectedName = centerName;
    nextTick(() => {
      const centerEl = nameToEl.get(centerName);
      if (centerEl) {
        const activeRing = centerEl.querySelector(".node-active-ring") as SVGCircleElement | null;
        if (activeRing) activeRing.style.opacity = "1";
        showNodeLabel(centerEl, true);
      }
      fitToView();
      openDetailDrawer(centerName);
    });
  } catch (e: any) {
    MessagePlugin.error(e?.message || "加载子图失败");
  } finally {
    graphLoading.value = false;
  }
}

// 设置活动中心节点 → 从后端加载完整的 N 跳子图
// （不再是前端 BFS 过滤，因为前端只加载了 top-N 节点，很多邻居不在视图中）
function setActiveCenter(name: string) {
  activeCenterNode.value = name;
  hasActiveFilter.value = true;
  hasFuzzyFilter.value = false;
  clearHighlight();
  loadEgoGraph(name, hopCount.value);
}

function onHopChange(val: number) {
  hopCount.value = val as 1 | 2 | 3;
  if (activeCenterNode.value && hasActiveFilter.value) {
    loadEgoGraph(activeCenterNode.value, hopCount.value);
  }
}

// ===== 详情抽屉 =====
async function openDetailDrawer(name: string) {
  detailLoading.value = true;
  detailDrawerVisible.value = true;
  try {
    const res: any = await getNeo4jNodeDetail(props.knowledgeBaseId, name);
    detailNode.value = res?.data || res;
  } catch (e: any) {
    MessagePlugin.error(e?.message || "获取节点详情失败");
  } finally {
    detailLoading.value = false;
  }
}

function onNeighborClick(name: string) {
  detailDrawerVisible.value = false;
  setActiveCenter(name);
}

function getNeighborDotColor(degree: number): string {
  if (maxDegreeInData <= 0) return "#722ed1";
  const pct = degree / maxDegreeInData;
  if (pct >= 0.95) return "#d54941";
  if (pct >= 0.8) return "#e37318";
  if (pct >= 0.55) return "#722ed1";
  if (pct >= 0.3) return "#0594fa";
  return "#2ba471";
}

// ===== Setup =====
function setupSvg() {
  if (!graphRef.value) return;
  const NS = "http://www.w3.org/2000/svg";
  graphSvg = document.createElementNS(NS, "svg");
  graphSvg.setAttribute("class", "neo4j-graph-svg");
  graphSvg.style.width = "100%";
  graphSvg.style.height = "100%";
  graphSvg.style.display = "block";
  graphRef.value.appendChild(graphSvg);

  // defs: 箭头标记（与 Wiki 图谱一致的箭头形状）
  const defs = document.createElementNS(NS, "defs");
  graphSvg.appendChild(defs);

  // 单向箭头（末端）
  const arrowEnd = document.createElementNS(NS, "marker");
  arrowEnd.setAttribute("id", "arrow-end");
  arrowEnd.setAttribute("viewBox", "0 0 10 6");
  arrowEnd.setAttribute("refX", "10");
  arrowEnd.setAttribute("refY", "3");
  arrowEnd.setAttribute("markerWidth", "8");
  arrowEnd.setAttribute("markerHeight", "6");
  arrowEnd.setAttribute("orient", "auto");
  const arrowPath1 = document.createElementNS(NS, "path");
  arrowPath1.setAttribute("d", "M0,0 L10,3 L0,6 L2,3 Z");
  arrowPath1.setAttribute("fill", EDGE_COLOR);
  arrowEnd.appendChild(arrowPath1);
  defs.appendChild(arrowEnd);

  // 单向箭头（始端，用于双向边反方向）
  const arrowStart = document.createElementNS(NS, "marker");
  arrowStart.setAttribute("id", "arrow-start");
  arrowStart.setAttribute("viewBox", "0 0 10 6");
  arrowStart.setAttribute("refX", "0");
  arrowStart.setAttribute("refY", "3");
  arrowStart.setAttribute("markerWidth", "8");
  arrowStart.setAttribute("markerHeight", "6");
  arrowStart.setAttribute("orient", "auto");
  const arrowPath2 = document.createElementNS(NS, "path");
  arrowPath2.setAttribute("d", "M10,0 L0,3 L10,6 L8,3 Z");
  arrowPath2.setAttribute("fill", EDGE_COLOR);
  arrowStart.appendChild(arrowPath2);
  defs.appendChild(arrowStart);

  // 高亮箭头（末端）
  const arrowEndHl = document.createElementNS(NS, "marker");
  arrowEndHl.setAttribute("id", "arrow-end-hl");
  arrowEndHl.setAttribute("viewBox", "0 0 10 6");
  arrowEndHl.setAttribute("refX", "10");
  arrowEndHl.setAttribute("refY", "3");
  arrowEndHl.setAttribute("markerWidth", "9");
  arrowEndHl.setAttribute("markerHeight", "7");
  arrowEndHl.setAttribute("orient", "auto");
  const arrowPath3 = document.createElementNS(NS, "path");
  arrowPath3.setAttribute("d", "M0,0 L10,3 L0,6 L2,3 Z");
  arrowPath3.setAttribute("fill", EDGE_HIGHLIGHT_COLOR);
  arrowEndHl.appendChild(arrowPath3);
  defs.appendChild(arrowEndHl);

  // 高亮箭头（始端）
  const arrowStartHl = document.createElementNS(NS, "marker");
  arrowStartHl.setAttribute("id", "arrow-start-hl");
  arrowStartHl.setAttribute("viewBox", "0 0 10 6");
  arrowStartHl.setAttribute("refX", "0");
  arrowStartHl.setAttribute("refY", "3");
  arrowStartHl.setAttribute("markerWidth", "9");
  arrowStartHl.setAttribute("markerHeight", "7");
  arrowStartHl.setAttribute("orient", "auto");
  const arrowPath4 = document.createElementNS(NS, "path");
  arrowPath4.setAttribute("d", "M10,0 L0,3 L10,6 L8,3 Z");
  arrowPath4.setAttribute("fill", EDGE_HIGHLIGHT_COLOR);
  arrowStartHl.appendChild(arrowPath4);
  defs.appendChild(arrowStartHl);

  graphSvg.addEventListener("mousedown", onSvgMouseDown);
  graphSvg.addEventListener("wheel", onWheel, { passive: false });
  window.addEventListener("mousemove", onWindowMouseMove);
  window.addEventListener("mouseup", onWindowMouseUp);
}

onMounted(async () => {
  setupSvg();
  await nextTick();
  loadFullGraph();
});

onUnmounted(() => {
  cancelAnimationFrame(graphAnimFrame);
  window.removeEventListener("mousemove", onWindowMouseMove);
  window.removeEventListener("mouseup", onWindowMouseUp);
  if (clickTimer) clearTimeout(clickTimer);
  if (hoverLeaveTimer) clearTimeout(hoverLeaveTimer);
  if (searchDebounceTimer) clearTimeout(searchDebounceTimer);
});

// 暴露给模板的常量
const nodeColorTiers = NODE_COLOR_TIERS;
</script>

<template>
  <div class="neo4j-graph-viewer">
    <!-- 搜索浮层（左上角，Wiki 图谱风格） -->
    <div class="graph-search-panel">
      <t-select
        v-model="searchValue"
        filterable
        :options="searchOptions.map((n) => ({
          label: n.name + `  ·  ${n.degree}°`,
          value: n.name,
        }))"
        :loading="searchLoading"
        :on-search="onSearchInput"
        placeholder="搜索实体节点（支持模糊匹配）..."
        :popup-props="{ zIndex: 100 }"
        clearable
        @change="onSearchSelect"
        @clear="clearFilter"
        class="graph-search-select"
      >
        <template #prefixIcon><t-icon name="search" /></template>
      </t-select>
      <div v-if="hasActiveFilter || hasFuzzyFilter" class="hop-control">
        <span v-if="hasActiveFilter" class="hop-label">跳数</span>
        <t-radio-group v-if="hasActiveFilter" :value="hopCount" variant="default-filled" size="small" @change="onHopChange">
          <t-radio-button :value="1">1跳</t-radio-button>
          <t-radio-button :value="2">2跳</t-radio-button>
          <t-radio-button :value="3">3跳</t-radio-button>
        </t-radio-group>
        <span v-if="hasFuzzyFilter && !hasActiveFilter" class="fuzzy-hint">
          模糊匹配：{{ fuzzyMatchCount }} 个节点
        </span>
        <t-link theme="primary" size="small" @click="clearFilter" class="clear-filter-btn">
          显示全部
        </t-link>
      </div>
    </div>

    <!-- 右上角操作按钮 -->
    <div class="graph-actions-panel">
      <div class="action-btn" :class="{ active: showAllLabels }" @click="showAllLabelsToggle" title="显示全部标签">
        <t-icon name="font" size="18px" />
        <span class="action-tip">{{ showAllLabels ? '隐藏标签' : '显示全部标签' }}</span>
      </div>
      <div class="action-btn" @click="fitToView" title="适应屏幕">
        <t-icon name="fullscreen" size="18px" />
        <span class="action-tip">适应屏幕</span>
      </div>
    </div>

    <!-- 图图画布（充满整个区域） -->
    <div ref="graphRef" class="neo4j-graph-canvas">
      <div v-if="graphLoading" class="graph-loading">
        <t-loading size="large" text="加载图谱中..." />
      </div>
      <div v-if="!graphLoading && graphReady && (!graphData || graphData.nodes.length === 0)" class="graph-empty">
        <t-empty description="暂无图谱数据" />
      </div>
    </div>

    <!-- 底部图例 + 统计 -->
    <div class="graph-footer">
      <div class="legend-section">
        <span class="legend-title">节点度数</span>
        <span v-for="(tier, i) in nodeColorTiers" :key="i" class="legend-item">
          <span class="legend-dot" :style="{ background: tier.color }"></span>
          {{ tier.label }}
        </span>
      </div>
      <div class="stats-text">{{ statsText }}</div>
    </div>

    <!-- 详情抽屉 -->
    <t-drawer
      v-model:visible="detailDrawerVisible"
      :header="detailNode?.name || '节点详情'"
      size="400px"
      placement="right"
    >
      <div v-if="detailLoading" class="detail-loading">
        <t-loading text="加载中..." />
      </div>
      <div v-else-if="detailNode" class="detail-content">
        <div class="detail-section">
          <h4>基本信息</h4>
          <div class="detail-metrics">
            <div class="metric-item">
              <div class="metric-value">{{ detailNode.degree }}</div>
              <div class="metric-label">连接度数</div>
            </div>
            <div class="metric-item">
              <div class="metric-value">{{ detailNode.neighbor_count }}</div>
              <div class="metric-label">邻居数</div>
            </div>
            <div class="metric-item">
              <div class="metric-value">{{ detailNode.chunk_count }}</div>
              <div class="metric-label">关联片段</div>
            </div>
          </div>
        </div>

        <div v-if="detailNode.attributes?.length" class="detail-section">
          <h4>属性</h4>
          <div class="attr-list">
            <t-tag v-for="(attr, i) in detailNode.attributes" :key="i" size="small" theme="primary" variant="light">
              {{ attr }}
            </t-tag>
          </div>
        </div>

        <div class="detail-section">
          <h4>关系类型分布</h4>
          <div class="rel-type-list">
            <div v-for="(count, type) in detailNode.rel_types" :key="type" class="rel-type-row">
              <span class="rel-type-name" :title="type">{{ type }}</span>
              <div class="rel-type-bar">
                <div
                  class="rel-type-bar-fill"
                  :style="{ width: Math.round((count / detailNode.degree) * 100) + '%' }"
                ></div>
              </div>
              <span class="rel-type-count">{{ count }}</span>
            </div>
          </div>
        </div>

        <div v-if="detailNode.top_neighbors?.length" class="detail-section">
          <h4>Top 邻居（点击展开）</h4>
          <div class="neighbor-list">
            <div
              v-for="neighbor in detailNode.top_neighbors"
              :key="neighbor.name"
              class="neighbor-item"
              @click="onNeighborClick(neighbor.name)"
            >
              <span
                class="neighbor-dot"
                :style="{ background: getNeighborDotColor(neighbor.degree) }"
              ></span>
              <span class="neighbor-name" :title="neighbor.name">{{ neighbor.name }}</span>
              <span class="neighbor-degree">{{ neighbor.degree }}°</span>
              <t-icon name="chevron-right" size="14px" class="neighbor-arrow" />
            </div>
          </div>
        </div>
      </div>
    </t-drawer>
  </div>
</template>

<style scoped>
.neo4j-graph-viewer {
  position: relative;
  width: 100%;
  height: 100%;
  background: #fff;
  overflow: hidden;
}

.neo4j-graph-canvas {
  width: 100%;
  height: 100%;
  position: absolute;
  top: 0;
  left: 0;
}

.neo4j-graph-svg {
  cursor: grab;
  background: #fff;
}

/* 搜索浮层 */
.graph-search-panel {
  position: absolute;
  top: 16px;
  left: 16px;
  z-index: 10;
  display: flex;
  flex-direction: column;
  gap: 10px;
  min-width: 280px;
  max-width: 360px;
  background: #fff;
  padding: 12px;
  border-radius: 8px;
  box-shadow: 0 2px 12px rgba(0,0,0,0.08);
  border: 1px solid #e8e8e8;
}

.graph-search-select {
  width: 100%;
}

.fuzzy-hint {
  font-size: 12px;
  color: #722ed1;
  font-weight: 500;
}

.hop-control {
  display: flex;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}

.hop-label {
  font-size: 12px;
  color: #666;
  font-weight: 500;
}

.clear-filter-btn {
  margin-left: auto;
  font-size: 12px;
}

/* 右上角操作面板 */
.graph-actions-panel {
  position: absolute;
  top: 16px;
  right: 16px;
  z-index: 10;
  display: flex;
  flex-direction: column;
  gap: 8px;
  background: #fff;
  border-radius: 8px;
  padding: 6px;
  box-shadow: 0 2px 12px rgba(0,0,0,0.08);
  border: 1px solid #e8e8e8;
}

.action-btn {
  width: 36px;
  height: 36px;
  display: flex;
  align-items: center;
  justify-content: center;
  border-radius: 6px;
  cursor: pointer;
  color: #555;
  transition: all 0.15s;
  position: relative;
}

.action-btn:hover,
.action-btn.active {
  background: #f0f2f5;
  color: #722ed1;
}

.action-btn.active {
  background: #f5f3ff;
}

.action-tip {
  position: absolute;
  right: calc(100% + 8px);
  top: 50%;
  transform: translateY(-50%);
  background: #333;
  color: #fff;
  padding: 4px 8px;
  border-radius: 4px;
  font-size: 12px;
  white-space: nowrap;
  opacity: 0;
  pointer-events: none;
  transition: opacity 0.15s;
}

.action-btn:hover .action-tip {
  opacity: 1;
}

/* 加载 / 空状态 */
.graph-loading {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  z-index: 5;
  background: rgba(255,255,255,0.9);
  padding: 20px 30px;
  border-radius: 10px;
}

.graph-empty {
  position: absolute;
  top: 50%;
  left: 50%;
  transform: translate(-50%, -50%);
  z-index: 5;
}

/* 底部图例 + 统计 */
.graph-footer {
  position: absolute;
  bottom: 12px;
  left: 16px;
  right: 16px;
  z-index: 10;
  display: flex;
  justify-content: space-between;
  align-items: center;
  background: rgba(255,255,255,0.92);
  backdrop-filter: blur(8px);
  padding: 8px 14px;
  border-radius: 8px;
  box-shadow: 0 1px 8px rgba(0,0,0,0.06);
  border: 1px solid #ebeef2;
  font-size: 12px;
}

.legend-section {
  display: flex;
  align-items: center;
  gap: 12px;
  flex-wrap: wrap;
}

.legend-title {
  font-weight: 600;
  color: #333;
  margin-right: 2px;
}

.legend-item {
  display: flex;
  align-items: center;
  gap: 5px;
  color: #555;
}

.legend-dot {
  width: 9px;
  height: 9px;
  border-radius: 50%;
  border: 1.5px solid #fff;
  box-shadow: 0 1px 2px rgba(0,0,0,0.2);
}

.stats-text {
  color: #888;
  font-variant-numeric: tabular-nums;
}

/* 详情抽屉 */
.detail-content {
  padding: 4px 0;
}

.detail-section {
  margin-bottom: 24px;
}

.detail-section h4 {
  margin: 0 0 12px 0;
  font-size: 14px;
  font-weight: 600;
  color: #222;
}

.detail-metrics {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 8px;
}

.metric-item {
  background: #f7f8fa;
  border-radius: 6px;
  padding: 12px 8px;
  text-align: center;
}

.metric-value {
  font-size: 20px;
  font-weight: 700;
  color: #722ed1;
  margin-bottom: 4px;
}

.metric-label {
  font-size: 12px;
  color: #999;
}

.attr-list {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}

.rel-type-list {
  display: flex;
  flex-direction: column;
  gap: 7px;
}

.rel-type-row {
  display: flex;
  align-items: center;
  gap: 10px;
  font-size: 12px;
}

.rel-type-name {
  width: 80px;
  flex-shrink: 0;
  color: #555;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.rel-type-bar {
  flex: 1;
  height: 6px;
  background: #f0f0f0;
  border-radius: 3px;
  overflow: hidden;
}

.rel-type-bar-fill {
  height: 100%;
  background: linear-gradient(90deg, #722ed1, #9254de);
  border-radius: 3px;
  transition: width 0.3s;
}

.rel-type-count {
  width: 28px;
  text-align: right;
  color: #999;
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}

.neighbor-list {
  max-height: 320px;
  overflow-y: auto;
  border: 1px solid #f0f0f0;
  border-radius: 6px;
}

.neighbor-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 9px 12px;
  cursor: pointer;
  transition: background 0.15s;
  border-bottom: 1px solid #f5f5f5;
}

.neighbor-item:last-child {
  border-bottom: none;
}

.neighbor-item:hover {
  background: #f5f3ff;
}

.neighbor-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}

.neighbor-name {
  flex: 1;
  font-size: 13px;
  color: #333;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.neighbor-degree {
  font-size: 12px;
  color: #999;
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}

.neighbor-arrow {
  color: #ccc;
  flex-shrink: 0;
}

.detail-loading {
  display: flex;
  justify-content: center;
  padding: 40px 0;
}
</style>
