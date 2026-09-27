// Barrel so `@/components/forms` keeps working as one import surface for consumers (pages, ViewsMenu),
// while each entity's form lives in its own file - this directory replaced a single ~930-line forms.tsx
// that had grown to hold six unrelated forms plus their shared bits in one place.
export { ClusterForm } from './ClusterForm'
export { NodeForm } from './NodeForm'
export { ServiceForm } from './ServiceForm'
export { DeviceForm } from './DeviceForm'
export { ApplicationForm } from './ApplicationForm'
export { SiteForm } from './SiteForm'
export { ConfirmModal } from './ConfirmModal'
