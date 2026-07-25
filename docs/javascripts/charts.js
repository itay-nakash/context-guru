// Interactive benchmark charts (Chart.js via CDN). Numbers are inlined here from
// docs/results/comparison.md (SWE-bench Verified, claude-code agent on
// aws/claude-sonnet-5, matched 48-task set). A <canvas data-cg-chart="NAME"> in
// the markdown is rendered by the matching function below.
// ponytail: inline data, not wired to the results CSV — swap to a fetch of
// deploy/eval-containers/results/*.csv if the numbers should track the sweep automatically.

// Matched 48-task totals, from docs/results/comparison.md.
const CG_ARMS = ["baseline", "context-guru", "headroom"];
const CG_COST = [29.73, 25.71, 28.19]; // total billed $ (lower is better)
const CG_CACHE_READ = [96.8, 80.6, 91.1]; // cache-read tokens, millions (lower is better)

function cgTeal(alpha) {
  return `rgba(0, 150, 136, ${alpha})`;
}
function cgGrey(alpha) {
  return `rgba(120, 130, 140, ${alpha})`;
}
function cgArmColors(alpha) {
  // context-guru (index 1) highlighted in teal; the other arms muted grey.
  return CG_ARMS.map((_, i) => (i === 1 ? cgTeal(alpha) : cgGrey(alpha)));
}

function cgGridColor() {
  const dark = document.body.getAttribute("data-md-color-scheme") === "slate";
  return dark ? "rgba(255,255,255,0.10)" : "rgba(0,0,0,0.08)";
}
function cgTickColor() {
  const dark = document.body.getAttribute("data-md-color-scheme") === "slate";
  return dark ? "rgba(255,255,255,0.72)" : "rgba(0,0,0,0.72)";
}

function cgBar(canvas, data, title, unit) {
  if (canvas._cgChart) canvas._cgChart.destroy();
  canvas._cgChart = new Chart(canvas, {
    type: "bar",
    data: {
      labels: CG_ARMS,
      datasets: [
        {
          data,
          backgroundColor: cgArmColors(0.85),
          borderRadius: 4,
        },
      ],
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: { display: false },
        title: { display: true, text: title, color: cgTickColor() },
        tooltip: {
          callbacks: { label: (i) => ` ${i.parsed.y}${unit}` },
        },
      },
      scales: {
        y: {
          beginAtZero: true,
          title: { display: true, text: unit, color: cgTickColor() },
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
    switch (canvas.dataset.cgChart) {
      case "billed-cost":
        cgBar(canvas, CG_COST, "Total billed cost (matched 48 tasks)", " $");
        break;
      case "cache-read":
        cgBar(canvas, CG_CACHE_READ, "Cache-read tokens (matched 48 tasks)", " M");
        break;
    }
  });
}

// Render on load and after Material instant-nav swaps + palette changes.
if (typeof document$ !== "undefined") {
  document$.subscribe(() => setTimeout(cgRenderAll, 0));
} else {
  window.addEventListener("DOMContentLoaded", cgRenderAll);
}
