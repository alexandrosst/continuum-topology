import { FusionPanel, useFusion } from '@/components/fusion/useFusion'
import { PageHeader } from '@/components/ui/primitives'

// FUSION as a section of its own (nested routes under /fusion/*: overview, data, access, settings).
// Placeholder: the existing panel, until the section is built.
export default function FusionPage() {
  const fusion = useFusion(true)
  return (
    <div>
      <PageHeader title="FUSION" />
      <FusionPanel fusion={fusion} />
    </div>
  )
}
