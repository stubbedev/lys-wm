import { lysConfig } from 'lys-docs-theme/config'

export default lysConfig({
  name: 'lys-wm',
  description: 'A tiling Wayland compositor in pure Go, with hy3-style tiling and a full desktop session.',
  nav: [
    { text: 'Guide', link: '/guide/getting-started' },
    { text: 'Config', link: '/config' },
    { text: 'Architecture', link: '/architecture' },
  ],
  sidebar: [
    {
      text: 'Guide',
      items: [
        { text: 'Getting started', link: '/guide/getting-started' },
        { text: 'Install', link: '/guide/install' },
        { text: 'Scope', link: '/guide/scope' },
        { text: 'Upgrading in place', link: '/guide/upgrade' },
      ],
    },
    {
      text: 'Reference',
      items: [
        { text: 'Config', link: '/config' },
        { text: 'Actions', link: '/actions' },
        { text: 'IPC', link: '/ipc' },
        { text: 'Screen shaders', link: '/shaders' },
      ],
    },
    {
      text: 'Contributors',
      items: [
        { text: 'Contributing', link: '/guide/contributing' },
        { text: 'Architecture', link: '/architecture' },
        { text: 'Frame pacing', link: '/frame-pacing' },
      ],
    },
  ],
})
