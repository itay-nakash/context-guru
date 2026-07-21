// MathJax config for pymdownx.arithmatex (generic mode) — renders the phi_evict Φ formula.
window.MathJax = {
  tex: {
    inlineMath: [["\\(", "\\)"]],
    displayMath: [["\\[", "\\]"], ["$$", "$$"]],
    processEscapes: true,
    processEnvironments: true,
  },
  options: { ignoreHtmlClass: ".*|", processHtmlClass: "arithmatex" },
};

// Re-typeset after Material's instant navigation swaps the page body.
document$.subscribe(() => {
  if (window.MathJax && window.MathJax.typesetPromise) {
    window.MathJax.typesetPromise();
  }
});
