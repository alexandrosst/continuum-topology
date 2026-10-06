// Runs before first paint (see index.html). Mirrors src/lib/theme.ts's storage key and values.
(function () {
  try {
    var t = localStorage.getItem('continuum:theme')
    if (t !== 'light' && t !== 'dark') {
      // No pinned choice: resolve the OS preference now, in JS, rather than leaving data-theme absent.
      t = window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
    }
    document.documentElement.setAttribute('data-theme', t)
    document.documentElement.style.colorScheme = t
  } catch (e) {}
})()
