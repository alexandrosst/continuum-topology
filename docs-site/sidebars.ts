import type {SidebarsConfig} from '@docusaurus/plugin-content-docs';

// Hand-written rather than generated. The order is the order of a reader's questions: what is this,
// can I get it running, how do I use it, how is it built, and what do I look up. An alphabetical
// sidebar would flatten that right back out.
const sidebars: SidebarsConfig = {
  docsSidebar: [
    'intro',
    {
      type: 'category',
      label: 'Learn',
      collapsed: false,
      link: {type: 'doc', id: 'learn/what-is-ikhnos'},
      items: ['learn/what-is-ikhnos', 'learn/how-it-works', 'learn/concepts'],
    },
    {
      type: 'category',
      label: 'Get started',
      collapsed: false,
      link: {type: 'doc', id: 'journey/index'},
      items: [
        'journey/index',
        'journey/prepare',
        'journey/install-server',
        'journey/sign-in',
        'journey/connect-a-cluster',
        'journey/approve',
        'journey/explore',
        'journey/advice',
        'journey/next-steps',
        'getting-started/quickstart',
      ],
    },
    {
      type: 'category',
      label: 'Guides',
      link: {type: 'doc', id: 'user-guide/using-the-ui'},
      items: [
        'user-guide/using-the-ui',
        'user-guide/approvals-in-depth',
        'user-guide/mobility-and-placement',
        'user-guide/namespaces-and-services',
        'installation/index',
        'installation/production-cluster',
        'installation/connecting-a-cluster',
      ],
    },
    {
      type: 'category',
      label: 'Inside Ikhnos',
      link: {type: 'doc', id: 'inside/index'},
      items: [
        'inside/index',
        'architecture/overview',
        'inside/server',
        'inside/agent',
        'inside/data-flow',
        'inside/config-channel',
        'inside/placement-engine',
        'inside/frontend',
        {
          type: 'category',
          label: 'Trust and security',
          items: ['architecture/trust-model', 'architecture/agent-trust-model', 'architecture/permissions-reference'],
        },
        {
          type: 'category',
          label: 'Telemetry and FUSION',
          items: [
            'architecture/observability-intent',
            'architecture/telemetry-intent',
            'architecture/regional-operators',
            'architecture/regional-operators-lifecycle',
            'architecture/fusion-api',
            'architecture/fusion-grafana',
          ],
        },
        {
          type: 'category',
          label: 'Links and memory',
          items: ['architecture/cluster-links', 'architecture/system-memory'],
        },
        {
          type: 'category',
          label: 'Operating',
          items: ['architecture/exposure-options', 'architecture/lifecycle'],
        },
        'inside/design-history',
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
      items: ['reference/index', 'reference/server-helm-values', 'reference/agent-helm-values', 'reference/cli-flags'],
    },
    {
      type: 'category',
      label: 'Contributing',
      link: {type: 'doc', id: 'contributing/developer-guide'},
      items: ['contributing/developer-guide', 'contributing/release-process'],
    },
    {
      type: 'category',
      label: 'Project',
      link: {type: 'doc', id: 'project/status-and-limits'},
      items: ['project/status-and-limits', 'project/roadmap'],
    },
  ],
};

export default sidebars;
