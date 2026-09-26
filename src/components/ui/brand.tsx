import clsx from 'clsx'
import { Building2, Cpu } from 'lucide-react'
import { useEffect, useState, type ReactNode } from 'react'
import {
  siAlibabacloud, siDigitalocean, siGooglecloud, siHetzner, siK3s, siKubernetes, siOpenstack, siOvh, siProxmox, siRancher, siRedhatopenshift, siScaleway, siTalos, siVmware, siVultr,
} from 'simple-icons'
import { countryName, distroKey, placeLabel, providerKey, type DistroKey, type ProviderKey } from '@/lib/present'
import type { Site } from '@/lib/types'

/* ---------- ownership ---------- */

/** Who the product belongs to, shown in the footer of the menu and of the sign-in page. Change it here. */
export const OWNER = 'Continuum Topology Studio'

export function Copyright({ className }: { className?: string }) {
  return (
    <p className={clsx('text-[11px] leading-snug text-nb-600', className)} data-testid="copyright">
      © {new Date().getFullYear()} {OWNER}. All rights reserved.
    </p>
  )
}

/* ---------- logos ---------- */

type SimpleIcon = { title: string; hex: string; path: string }
type Tile = { text: string; hex: string; title: string }

/** Brand colours that are too dark to read on the dark UI are lifted towards white. */
function readable(hex: string): string {
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16) / 255)
  const lum = 0.2126 * r + 0.7152 * g + 0.0722 * b
  if (lum >= 0.22) return `#${hex}`
  const mix = (c: number) => Math.round((c + (1 - c) * 0.55) * 255).toString(16).padStart(2, '0')
  return `#${mix(r)}${mix(g)}${mix(b)}`
}

function Glyph({ icon, size }: { icon: SimpleIcon; size: number }) {
  return (
    <svg role="img" aria-label={icon.title} viewBox="0 0 24 24" width={size} height={size} fill={readable(icon.hex)} className="shrink-0">
      <title>{icon.title}</title>
      <path d={icon.path} />
    </svg>
  )
}

/** A lettered tile for products that have no logo in the icon set: honest and consistent. */
function LetterTile({ tile, size }: { tile: Tile; size: number }) {
  const c = readable(tile.hex)
  return (
    <span
      role="img"
      aria-label={tile.title}
      title={tile.title}
      className="inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded-[5px] border font-semibold leading-none tracking-tight"
      style={{
        // The box grows with its text (min = the square), so a label like "kind" or "EKS" can never spill out of it.
        minWidth: size,
        height: size,
        padding: '0 3px',
        fontSize: Math.max(8, Math.round(size * 0.4)),
        color: c,
        borderColor: `color-mix(in srgb, ${c} 40%, transparent)`,
        background: `color-mix(in srgb, ${c} 14%, transparent)`,
      }}
    >
      {tile.text}
    </span>
  )
}

const DISTRO: Record<DistroKey, { icon: SimpleIcon } | { tile: Tile }> = {
  eks: { tile: { text: 'EKS', hex: 'FF9900', title: 'Amazon EKS' } },
  gke: { tile: { text: 'GKE', hex: '4285F4', title: 'Google GKE' } },
  aks: { tile: { text: 'AKS', hex: '0078D4', title: 'Azure AKS' } },
  k3s: { icon: siK3s },
  rke2: { icon: siRancher },
  openshift: { icon: siRedhatopenshift },
  microk8s: { tile: { text: 'M8s', hex: 'E95420', title: 'MicroK8s' } },
  talos: { icon: siTalos },
  k0s: { tile: { text: 'k0s', hex: '326CE5', title: 'k0s' } },
  kind: { tile: { text: 'kind', hex: '326CE5', title: 'kind / minikube' } },
  kubeadm: { icon: siKubernetes },
  kubernetes: { icon: siKubernetes },
}

export function DistroIcon({ distribution, size = 20 }: { distribution?: string; size?: number }) {
  const d = DISTRO[distroKey(distribution)]
  return 'icon' in d ? <Glyph icon={d.icon} size={size} /> : <LetterTile tile={d.tile} size={size} />
}

const PROVIDER: Record<ProviderKey, { icon: SimpleIcon } | { tile: Tile } | { lucide: 'building' | 'cpu' } | null> = {
  aws: { tile: { text: 'AWS', hex: 'FF9900', title: 'AWS' } },
  azure: { tile: { text: 'Az', hex: '0078D4', title: 'Microsoft Azure' } },
  gcp: { icon: siGooglecloud },
  hetzner: { icon: siHetzner },
  digitalocean: { icon: siDigitalocean },
  ovh: { icon: siOvh },
  scaleway: { icon: siScaleway },
  vultr: { icon: siVultr },
  alibaba: { icon: siAlibabacloud },
  openstack: { icon: siOpenstack },
  vmware: { icon: siVmware },
  proxmox: { icon: siProxmox },
  'on-prem': { lucide: 'building' },
  edge: { lucide: 'cpu' },
  unknown: null,
}

export function ProviderIcon({ provider, size = 20 }: { provider?: string; size?: number }) {
  const p = PROVIDER[providerKey(provider)]
  if (!p) return null
  if ('icon' in p) return <Glyph icon={p.icon} size={size} />
  if ('tile' in p) return <LetterTile tile={p.tile} size={size} />
  const L = p.lucide === 'building' ? Building2 : Cpu
  return <L size={size - 2} className="shrink-0 text-nb-400" aria-hidden />
}

/** Logo followed by a label, kept on one line. */
export function WithIcon({ icon, children, className }: { icon: ReactNode; children: ReactNode; className?: string }) {
  return (
    <span className={clsx('inline-flex items-center gap-2 whitespace-nowrap', className)}>
      {icon}
      {children}
    </span>
  )
}

/* ---------- flags ---------- */

type FlagModule = Record<string, React.ComponentType<React.SVGProps<SVGSVGElement> & { title?: string }>>
let flags: FlagModule | undefined
let loading: Promise<void> | undefined

/** Runs `fn` when the browser is next idle (falling back to a plain macrotask where that API is
 * missing, e.g. Safari), and returns a canceller - so a component that unmounts before its turn never
 * triggers the work at all. */
function onIdle(fn: () => void): () => void {
  if (typeof requestIdleCallback === 'function') {
    const id = requestIdleCallback(fn)
    return () => cancelIdleCallback(id)
  }
  const id = setTimeout(fn, 1)
  return () => clearTimeout(id)
}

/**
 * The flag set is large (~235KB), so it is fetched once, on first use, as its own chunk - and deferred to
 * idle time rather than fired the instant a `<Flag>` mounts, so it never competes with the initial paint
 * of a canvas that renders one for every node with country data (the common case on the default landing view).
 */
function useFlags(): FlagModule | undefined {
  const [mod, setMod] = useState(flags)
  useEffect(() => {
    if (flags) return void setMod(flags)
    let live = true
    const cancel = onIdle(() => {
      loading ??= import('country-flag-icons/react/3x2').then((m) => {
        flags = m as unknown as FlagModule
      })
      void loading.then(() => live && setMod(flags))
    })
    return () => {
      live = false
      cancel()
    }
  }, [])
  return mod
}

export function Flag({ code, className }: { code?: string; className?: string }) {
  const mod = useFlags()
  const c = (code ?? '').trim().toUpperCase()
  const name = countryName(c)
  const F = c.length === 2 ? mod?.[c] : undefined
  // Reserve the space either way so rows do not shift when the flag arrives.
  return (
    <span className={clsx('inline-block h-3 w-[18px] shrink-0 overflow-hidden rounded-[2px] align-middle ring-1 ring-white/10', className)} title={name || undefined}>
      {F && <F title={name} className="block h-full w-full" />}
    </span>
  )
}

/** "🇬🇷 Athens, Greece". Falls back to the free-text region, then to a dash. */
export function Place({ site, fallback, className }: { site?: Pick<Site, 'city' | 'country'>; fallback?: string; className?: string }) {
  const label = placeLabel(site)
  if (!label && !fallback) return <span className="text-nb-600">—</span>
  return (
    <span className={clsx('inline-flex items-center gap-2 whitespace-nowrap', className)}>
      {site?.country ? <Flag code={site.country} /> : null}
      <span>{label || fallback}</span>
    </span>
  )
}
