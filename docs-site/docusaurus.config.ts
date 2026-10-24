import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

// GitHub Pages project-page settings. If you ever rename the repo or move to a custom
// domain, these three plus the workflow's Pages source setting are the only things to change.
const GITHUB_OWNER = 'alexandrosst';
const GITHUB_REPO = 'ikhnos';

const config: Config = {
  title: 'Ikhnos',
  tagline: 'A read-only, agent-observed view of your clusters — from cloud to edge.',
  favicon: 'img/favicon.svg',

  future: {
    v4: true, // Improve compatibility with the upcoming Docusaurus v4
  },

  url: `https://${GITHUB_OWNER}.github.io`,
  baseUrl: `/${GITHUB_REPO}/`,

  organizationName: GITHUB_OWNER,
  projectName: GITHUB_REPO,

  onBrokenLinks: 'throw',
  onBrokenAnchors: 'warn',

  i18n: {
    defaultLocale: 'en',
    locales: ['en'],
  },

  headTags: [
    {
      tagName: 'link',
      attributes: {rel: 'preconnect', href: 'https://fonts.googleapis.com'},
    },
    {
      tagName: 'link',
      attributes: {rel: 'stylesheet', href: 'https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&display=swap'},
    },
  ],

  plugins: [
    [
      '@docusaurus/plugin-client-redirects',
      {
        // The docs used to be the whole site, at the root. Every old address still works.
        createRedirects(existingPath: string) {
          if (existingPath.startsWith('/docs/') && existingPath !== '/docs/') {
            return [existingPath.slice('/docs'.length)];
          }
          return undefined;
        },
      },
    ],
  ],

  themes: [
    [
      require.resolve('@easyops-cn/docusaurus-search-local'),
      {
        hashed: true,
        docsRouteBasePath: '/docs',
        indexBlog: false,
        highlightSearchTermsOnTargetPage: true,
        explicitSearchResultPath: true,
        searchResultLimits: 10,
      },
    ],
  ],

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: 'docs', // the landing page is src/pages/index.tsx; everything else is under /docs
          editUrl: `https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/tree/main/docs-site/`,
        },
        blog: false,
        theme: {
          customCss: ['./src/css/figure-tokens.css', './src/css/custom.css'],
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      // The product is dark first, so dark is the default. Anyone whose system asks for light gets light.
      defaultMode: 'dark',
      respectPrefersColorScheme: true,
    },
    navbar: {
      title: 'Ikhnos',
      logo: {
        alt: 'Ikhnos',
        src: 'img/logo.svg',
      },
      items: [
        {to: '/docs/learn/what-is-ikhnos', label: 'Learn', position: 'left', activeBasePath: '/docs/learn'},
        {to: '/docs/journey/', label: 'Get started', position: 'left', activeBasePath: '/docs/journey'},
        {to: '/docs/user-guide/using-the-ui', label: 'Guides', position: 'left', activeBasePath: '/docs/user-guide'},
        {to: '/docs/inside/', label: 'Inside Ikhnos', position: 'left', activeBasePath: '/docs/inside'},
        {to: '/docs/reference/', label: 'Reference', position: 'left', activeBasePath: '/docs/reference'},
        {type: 'search', position: 'right'},
        {
          href: `https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}`,
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    footer: {
      style: 'dark',
      links: [
        {
          title: 'Learn',
          items: [
            {label: 'What is Ikhnos', to: '/docs/learn/what-is-ikhnos'},
            {label: 'How it works', to: '/docs/learn/how-it-works'},
            {label: 'Concepts in plain words', to: '/docs/learn/concepts'},
          ],
        },
        {
          title: 'Use it',
          items: [
            {label: 'Your first hour', to: '/docs/journey/'},
            {label: 'Quickstart', to: '/docs/getting-started/quickstart'},
            {label: 'Troubleshooting', to: '/docs/troubleshooting/common-errors'},
          ],
        },
        {
          title: 'Build on it',
          items: [
            {label: 'Inside Ikhnos', to: '/docs/inside/'},
            {label: 'Contributing', to: '/docs/contributing/developer-guide'},
            {label: 'Roadmap', to: '/docs/project/roadmap'},
          ],
        },
        {
          title: 'Project',
          items: [
            {label: 'GitHub', href: `https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}`},
            {label: 'Releases', href: `https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/releases`},
            {label: 'License (Apache-2.0)', href: `https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/blob/main/LICENSE`},
          ],
        },
      ],
      copyright: `Ikhnos is open source under the Apache License 2.0.`,
    },
    prism: {
      theme: prismThemes.oneLight,
      darkTheme: prismThemes.oneDark,
      additionalLanguages: ['bash', 'go', 'yaml', 'diff'],
    },
    metadata: [
      {name: 'description', content: 'Documentation for Ikhnos: a read-only, agent-observed control plane for orchestration across the cloud-to-edge continuum.'},
    ],
  } satisfies Preset.ThemeConfig,
};

export default config;
