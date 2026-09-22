import {themes as prismThemes} from 'prism-react-renderer';
import type {Config} from '@docusaurus/types';
import type * as Preset from '@docusaurus/preset-classic';

// This runs in Node.js - Don't use client-side code here (browser APIs, JSX...)

// GitHub Pages project-page settings. If you ever rename the repo or move to a custom
// domain, these three plus the workflow's Pages source setting are the only things to change.
const GITHUB_OWNER = 'alexandrosst';
const GITHUB_REPO = 'continuum-topology';

const config: Config = {
  title: 'Continuum Topology Studio',
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

  presets: [
    [
      'classic',
      {
        docs: {
          sidebarPath: './sidebars.ts',
          routeBasePath: '/', // docs ARE the site — no separate landing page to maintain
          editUrl: `https://github.com/${GITHUB_OWNER}/${GITHUB_REPO}/tree/main/docs-site/`,
        },
        blog: false,
        theme: {
          customCss: './src/css/custom.css',
        },
      } satisfies Preset.Options,
    ],
  ],

  themeConfig: {
    colorMode: {
      // The product itself is dark-first (see src/index.css) and doesn't follow the OS preference either,
      // so the docs default to dark too rather than showing most first-time visitors a light theme this
      // product never actually has. The toggle in the navbar still lets anyone switch.
      defaultMode: 'dark',
      respectPrefersColorScheme: false,
    },
    navbar: {
      title: 'Continuum Topology Studio',
      logo: {
        alt: 'Continuum Topology Studio',
        src: 'img/logo.svg',
      },
      items: [
        {
          type: 'docSidebar',
          sidebarId: 'docsSidebar',
          position: 'left',
          label: 'Documentation',
        },
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
          title: 'Documentation',
          items: [
            {label: 'Getting started', to: '/getting-started/quickstart'},
            {label: 'Architecture', to: '/architecture/overview'},
            {label: 'Troubleshooting', to: '/troubleshooting/common-errors'},
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
      copyright: `Copyright © ${new Date().getFullYear()} ${GITHUB_OWNER}. Continuum Topology Studio is licensed under Apache-2.0.`,
    },
    prism: {
      theme: prismThemes.oneLight,
      darkTheme: prismThemes.oneDark,
      additionalLanguages: ['bash', 'go', 'yaml', 'diff'],
    },
    metadata: [
      {name: 'description', content: 'Documentation for Continuum Topology Studio: a read-only, agent-observed control plane for orchestration across the cloud-to-edge continuum.'},
    ],
  } satisfies Preset.ThemeConfig,
};

export default config;
