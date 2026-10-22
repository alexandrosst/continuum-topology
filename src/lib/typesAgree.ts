/**
 * The page writes the domain model by hand (types.ts) because the page's records carry more than the server's: what a
 * person declared, and the layer of overrides. The server's own shapes are generated from the Go types (generated/wire.ts).
 * This file holds the two to each other at compile time, so a field renamed, made optional or retyped on the Go side is a
 * type error here and not a screen that quietly shows nothing.
 *
 * For each pair, every field that both have must be the same type, optionality included, and every field the server sends
 * must be one the page has (or is listed below with the reason). A field only the page has is the page's own and is not
 * checked: those are the declared and overridden layers. Nothing here exists at run time.
 */
import type * as W from './generated/wire'
import type * as P from './types'

type Same<A, B> = (<T>() => T extends A ? 1 : 2) extends <T>() => T extends B ? 1 : 2 ? true : false
type Disagreeing<Page, Wire> = { [K in keyof Page & keyof Wire]-?: Same<Pick<Page, K>, Pick<Wire, K>> extends true ? never : K }[keyof Page & keyof Wire]
type WireOnly<Page, Wire> = Exclude<keyof Wire, keyof Page>
type Agrees<Page, Wire, Allowed extends PropertyKey = never> = [Exclude<Disagreeing<Page, Wire>, Allowed>, Exclude<WireOnly<Page, Wire>, Allowed>] extends [never, never]
  ? true
  : { disagree: Exclude<Disagreeing<Page, Wire>, Allowed>; theServerSendsAndThePageLacks: Exclude<WireOnly<Page, Wire>, Allowed> }
type Expect<T extends true> = T

/**
 * Where a pair is allowed to differ, and why:
 *  Service.readyReplicas: optional on the page, because a service a person declared has none; the server always sends it.
 *  Dependency.stats: the page's DependencyStats also has reqPerSec, errorRate and p95Ms, which the server does not send
 *    yet (the screens show them when a mesh or FUSION supplies them).
 *  Suggestion.apply: the server stores what applying a suggestion does as an opaque document; the page reads it as a
 *    SuggestionAction.
 * Site, SiteLink and Device are records only a person declares; the server keeps the workspace document as JSON and does
 * not read them, but writes their shape down (workspace/declared.go) so that they are generated and held here like the rest.
 * The page's Topology and Agent are not paired: the page's Topology also holds what a person declared (devices, sites,
 * applications) and the server's agents, suggestions and paths come to the page apart from it.
 */
export type Agreements = [
  Expect<Agrees<P.Evidence, W.Evidence>>,
  Expect<Agrees<P.Path, W.Path>>,
  Expect<Agrees<P.ExternalEndpoint, W.ExternalEndpoint>>,
  Expect<Agrees<P.ClusterLink, W.ClusterLink>>,
  Expect<Agrees<P.ClusterPairConnectivity, W.ClusterPairConnectivity>>,
  Expect<Agrees<P.Cluster, W.Cluster>>,
  Expect<Agrees<P.ClusterMesh, W.ClusterMesh>>,
  Expect<Agrees<P.ServiceMesh, W.ServiceMesh>>,
  Expect<Agrees<P.Accelerator, W.Accelerator>>,
  Expect<Agrees<P.NetworkInterface, W.NetworkInterface>>,
  Expect<Agrees<P.Disk, W.Disk>>,
  Expect<Agrees<P.TunnelInterface, W.TunnelInterface>>,
  Expect<Agrees<P.Resources, W.Resources>>,
  Expect<Agrees<P.LinkSaturation, W.LinkSaturation>>,
  Expect<Agrees<P.MachineNode, W.Node>>,
  Expect<Agrees<P.Namespace, W.Namespace>>,
  Expect<Agrees<P.Pod, W.Pod>>,
  Expect<Agrees<P.PodPeer, W.PodPeer>>,
  Expect<Agrees<P.Service, W.Service, 'readyReplicas'>>,
  Expect<Agrees<P.DependencyStats, W.DependencyStats>>,
  Expect<Agrees<P.Dependency, W.Dependency, 'stats'>>,
  Expect<Agrees<P.Suggestion, W.Suggestion, 'apply'>>,
  Expect<Agrees<P.Site, W.Site>>,
  Expect<Agrees<P.SiteLink, W.SiteLink>>,
  Expect<Agrees<P.Device, W.Device>>,
  Expect<Agrees<P.Application, W.Application>>,
]
