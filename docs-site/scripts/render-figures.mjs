// Writes the figures the README shows as static SVG files, one for light and one for dark, from the very
// components the documentation site draws. Run it from docs-site/ after changing a figure:
//
//   node scripts/render-figures.mjs
//
// A figure never names a colour; it uses the --cds-* variables of src/css/figure-tokens.css. GitHub shows an
// SVG as an image, where page CSS does not apply, so here each variable is replaced by its value for the theme.
import {createRequire} from 'node:module';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const site = path.resolve(here, '..');
const repo = path.resolve(site, '..');
const requireSite = createRequire(path.join(site, 'package.json'));
const requireRepo = createRequire(path.join(repo, 'package.json'));
const {build} = requireRepo('esbuild');
const {renderToStaticMarkup} = requireSite('react-dom/server');
const React = requireSite('react');

// README name -> figure name.
const PICK = {
  architecture: 'SystemAtAGlance',
  'trust-tiers': 'TrustChain',
  'placement-loop': 'PlacementPipeline',
};

const out = path.join(site, 'static', 'img', 'readme');
const tmp = fs.mkdtempSync(path.join(os.tmpdir(), 'ikh-figs-'));
const bundle = path.join(tmp, 'registry.cjs');
await build({
  entryPoints: [path.join(site, 'src/components/figures/registry.tsx')],
  bundle: true,
  platform: 'node',
  format: 'cjs',
  outfile: bundle,
  jsx: 'automatic',
  external: ['react', 'react-dom', 'clsx'],
  nodePaths: [path.join(site, 'node_modules')],
  logLevel: 'error',
});
const {figures} = createRequire(import.meta.url)(bundle);

const css = fs.readFileSync(path.join(site, 'src/css/figure-tokens.css'), 'utf8');
function vars(block) {
  const m = css.match(new RegExp(`${block.replace(/[[\]']/g, '\\$&')}\\s*\\{([^}]*)\\}`));
  return Object.fromEntries([...m[1].matchAll(/(--[\w-]+)\s*:\s*([^;]+);/g)].map((x) => [x[1], x[2].trim()]));
}
const light = vars(':root');
const dark = {...light, ...vars("[data-theme='dark']")};
const FONT = "Inter, -apple-system, 'Segoe UI', Helvetica, Arial, sans-serif";
const MONO = 'ui-monospace, SFMono-Regular, Menlo, Consolas, monospace';

for (const [file, name] of Object.entries(PICK)) {
  const markup = renderToStaticMarkup(React.createElement(figures[name]));
  for (const [theme, v] of [['light', light], ['dark', dark]]) {
    let svg = markup.replace(/var\(--ifm-font-family-monospace[^)]*\)/g, MONO);
    svg = svg.replace(/var\((--[\w-]+)(?:,[^)]*)?\)/g, (_, k) => {
      if (!(k in v)) throw new Error(`${name} uses ${k}, which figure-tokens.css does not define`);
      return v[k];
    });
    svg = svg.replace(/class="ikh-fig"/, `xmlns="http://www.w3.org/2000/svg" font-family="${FONT}"`);
    svg = svg.replace(/fontFamily/g, 'font-family');
    fs.writeFileSync(path.join(out, `${file}-${theme}.svg`), svg + '\n');
  }
  console.log('wrote', file, '<-', name);
}
fs.rmSync(tmp, {recursive: true, force: true});
