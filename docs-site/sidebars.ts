import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

// Hand-written rather than auto-generated: the whole point of this site is a structure that
// separates "I want to run this" from "I want to understand this" from "I want to hack on this" —
// an alphabetical autogen sidebar would flatten that distinction right back out.
const sidebars: SidebarsConfig = {
  docsSidebar: [
    'intro',
    {
      type: 'category',
      label: 'Getting started',
      link: {type: 'doc', id: 'getting-started/quickstart'},
      items: ['getting-started/quickstart'],
    },
    {
      type: 'category',
      label: 'Installation',
      link: {type: 'doc', id: 'installation/index'},
      items: [
        'installation/index',
        'installation/production-cluster',
        'installation/connecting-a-cluster',
      ],
    },
    {
      type: 'category',
      label: 'Architecture',
      link: {type: 'doc', id: 'architecture/overview'},
      items: [
        'architecture/overview',
        'architecture/agent-trust-model',
        'architecture/exposure-options',
      ],
    },
    {
      type: 'category',
      label: 'User guide',
      link: {type: 'doc', id: 'user-guide/using-the-ui'},
      items: [
        'user-guide/using-the-ui',
        'user-guide/approvals-in-depth',
        'user-guide/mobility-and-placement',
        'user-guide/namespaces-and-services',
      ],
    },
    {
      type: 'category',
      label: 'Troubleshooting',
      link: {type: 'doc', id: 'troubleshooting/common-errors'},
      items: ['troubleshooting/common-errors'],
    },
    {
      type: 'category',
      label: 'Reference',
      link: {type: 'doc', id: 'reference/index'},
      items: [
        'reference/index',
        'reference/server-helm-values',
        'reference/agent-helm-values',
        'reference/cli-flags',
      ],
    },
    {
      type: 'category',
      label: 'Contributing',
      link: {type: 'doc', id: 'contributing/developer-guide'},
      items: ['contributing/developer-guide', 'contributing/release-process'],
    },
  ],
};

export default sidebars;
