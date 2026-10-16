import type { ReactNode } from 'react'

import { AgentPanel } from '@/components/agent/agent-panel'
import { AppTopNav } from '@/components/layout/app-top-nav'

export default function UserLayout({ children }: { children: ReactNode }) {
  return (
    <div className='site-canvas-shell bg-background text-foreground relative flex h-dvh overflow-hidden'>
      <div className='flex min-w-0 flex-1 flex-col overflow-hidden'>
        <AppTopNav />
        <div className='min-h-0 flex-1 overflow-hidden'>{children}</div>
      </div>
      <AgentPanel />
    </div>
  )
}
