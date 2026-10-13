import { createFileRoute, redirect } from '@tanstack/react-router'

import { Main } from '@/components/layout'
import { InfiniteCanvasPage } from '@/features/infinite-canvas'
import { isSidebarModuleEnabled } from '@/lib/nav-modules'

export const Route = createFileRoute('/_authenticated/canvas/')({
  beforeLoad: () => {
    if (!isSidebarModuleEnabled('chat', 'canvas')) {
      throw redirect({ to: '/dashboard' })
    }
  },
  component: () => (
    <Main className='p-0'>
      <InfiniteCanvasPage />
    </Main>
  ),
})
