import React, { useEffect } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from 'react-router-dom'

import 'antd/dist/reset.css'
import 'streamdown/styles.css'
import '../styles/globals.css'
import '../styles/site-integration.css'
import { AppProviders } from '@/components/layout/app-providers'
import { router } from '@/router'

import { applySiteContext } from './sync-context'

applySiteContext()
window.addEventListener('message', (event: MessageEvent) => {
  if (
    event.source === window.parent &&
    event.origin === window.location.origin &&
    event.data?.type === 'new-api:canvas-context'
  )
    {applySiteContext()}
})

const root = document.querySelector('#root')
if (!root) throw new Error('Canvas root is missing')
export function CanvasApp() {
  useEffect(() => {
    window.parent.postMessage(
      { type: 'new-api:canvas-mounted' },
      window.location.origin
    )
  }, [])
  return (
    <AppProviders>
      <RouterProvider router={router} />
    </AppProviders>
  )
}

createRoot(root).render(
  <React.StrictMode>
    <CanvasApp />
  </React.StrictMode>
)
