import { createFileRoute, redirect } from '@tanstack/react-router'

import { CanvasViewport } from '@/features/infinite-canvas/persistent-canvas'
import { isSidebarModuleEnabled } from '@/lib/nav-modules'

export const Route = createFileRoute('/_authenticated/canvas/')({
  beforeLoad: () => {
    if (!isSidebarModuleEnabled('chat', 'canvas')) {
      throw redirect({ to: '/dashboard' })
    }
  },
  component: CanvasViewport,
})
