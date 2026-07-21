// Interactive benchmark charts (Chart.js via CDN). Data is inlined here from
// docs/RESULTS.md (SWE-bench, Claude Code, claude-sonnet-4-6, 10 tasks x 13 configs).
// A <canvas data-cg-chart="save-per-component"> in the markdown gets rendered here.
// ponytail: inline data, not wired to the results CSV — swap to a fetch of
// deploy/eval-containers/results/*.csv if the numbers should track the sweep automatically.

const CG_COMPONENTS = [
  { name: "mask", resolved: 26.8, all: 30.9 },
  { name: "balanced", resolved: 12.2, all: 9.0 },
  { name: "extract", resolved: 11.1, all: 11.4 },
  { name: "failed_run", resolved: 5.8, all: 7.3 },
  { name: "collapse", resolved: 0.0, all: 0.6 },
  { name: "dedup", resolved: 0.2, all: 0.3 },
  { name: "format", resolved: 0.0, all: 0.0 },
  { name: "cmdfilter", resolved: 0.0, all: 0.0 },
  { name: "cacheinject", resolved: 0.0, all: 0.0 },
  { name: "skeleton", resolved: 0.0, all: 0.0 },
  { name: "smartcrush", resolved: 0.0, all: 0.0 },
  { name: "phi_evict", resolved: 0.0, all: 0.0 },
];

function cgTeal(alpha) {
  return `rgba(0, 150, 136, ${alpha})`;
}

function cgGridColor() {
  const dark =
    document.body.getAttribute("data-md-color-scheme") === "slate";
  return dark ? "rgba(255,255,255,0.10)" : "rgba(0,0,0,0.08)";
}

function cgTickColor() {
  const dark =
    document.body.getAttribute("data-md-color-scheme") === "slate";
  return dark ? "rgba(255,255,255,0.72)" : "rgba(0,0,0,0.72)";
}

function renderSavePerComponent(canvas) {
  if (canvas._cgChart) canvas._cgChart.destroy();
  const labels = CG_COMPONENTS.map((c) => c.name);
  canvas._cgChart = new Chart(canvas, {
    type: "bar",
    data: {
      labels,
      datasets: [
        {
          label: "Resolved tasks (6)",
          data: CG_COMPONENTS.map((c) => c.resolved),
          backgroundColor: cgTeal(0.85),
          borderRadius: 4,
        },
        {
          label: "All tasks (10)",
          data: CG_COMPONENTS.map((c) => c.all),
          backgroundColor: cgTeal(0.35),
          borderRadius: 4,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { labels: { color: cgTickColor() } },
        title: {
          display: true,
          text: "Mean content-token savings % per component",
          color: cgTickColor(),
        },
        tooltip: {
          callbacks: { label: (i) => ` ${i.dataset.label}: ${i.parsed.y}%` },
        },
      },
      scales: {
        y: {
          beginAtZero: true,
          title: { display: true, text: "% tokens saved", color: cgTickColor() },
          grid: { color: cgGridColor() },
          ticks: { color: cgTickColor() },
        },
        x: { grid: { display: false }, ticks: { color: cgTickColor() } },
      },
    },
  });
}

function cgRenderAll() {
  document.querySelectorAll("canvas[data-cg-chart]").forEach((canvas) => {
    if (typeof Chart === "undefined") return;
    if (canvas.dataset.cgChart === "save-per-component") {
      renderSavePerComponent(canvas);
    }
  });
}

// Render on load and after Material instant-nav swaps + palette changes.
if (typeof document$ !== "undefined") {
  document$.subscribe(() => setTimeout(cgRenderAll, 0));
} else {
  window.addEventListener("DOMContentLoaded", cgRenderAll);
}
