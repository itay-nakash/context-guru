// MathJax config for pymdownx.arithmatex (generic mode) — renders inline/blocks math in the docs.
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
