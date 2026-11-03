import {
  createContext,
  lazy,
  Suspense,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'

import { Main } from '@/components/layout/components/main'
import { LoadingState } from '@/components/loading-state'
import { useSidebar } from '@/components/ui/sidebar'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

const CanvasPage = lazy(() =>
  import('./index').then((module) => ({ default: module.InfiniteCanvasPage }))
)

const CanvasViewportContext = createContext<
  (element: HTMLDivElement | null) => void
>(() => undefined)

/** The editor stays in one DOM location, including when public pages replace the console layout. */
export function PersistentCanvas(props: { children: ReactNode }) {
  const userId = useAuthStore((state) => state.auth.user?.id)
  const [viewport, setViewport] = useState<HTMLDivElement | null>(null)

  return (
    <CanvasViewportContext.Provider value={setViewport}>
      {props.children}
      {userId && <CanvasSession key={userId} viewport={viewport} />}
    </CanvasViewportContext.Provider>
  )
}

function CanvasSession(props: { viewport: HTMLDivElement | null }) {
  const [bounds, setBounds] = useState<{
    top: number
    left: number
    width: number
    height: number
  } | null>(null)

  useLayoutEffect(() => {
    const viewport = props.viewport
    if (!viewport) return

    const measure = () => {
      const rect = viewport.getBoundingClientRect()
      if (rect.width <= 0 || rect.height <= 0) return
      setBounds((previous) => {
        if (
          previous?.top === rect.top &&
          previous.left === rect.left &&
          previous.width === rect.width &&
          previous.height === rect.height
        ) {
          return previous
        }
        return {
          top: rect.top,
          left: rect.left,
          width: rect.width,
          height: rect.height,
        }
      })
    }

    measure()
    const observer = new ResizeObserver(measure)
    observer.observe(viewport)
    window.addEventListener('resize', measure)
    window.addEventListener('scroll', measure, true)
    return () => {
      observer.disconnect()
      window.removeEventListener('resize', measure)
      window.removeEventListener('scroll', measure, true)
    }
  }, [props.viewport])

  // No editor or catalog requests until the first visit. Keep its last valid
  // bounds while hidden; moving an iframe into a portal would reload it.
  if (!bounds) return null

  return (
    <Main
      hidden={!props.viewport}
      inert={!props.viewport}
      className={cn('bg-background fixed z-0 p-0', !props.viewport && 'hidden')}
      style={bounds}
    >
      <Suspense fallback={<LoadingState />}>
        <CanvasPage />
      </Suspense>
    </Main>
  )
}

/** Only the viewport and sidebar behavior belong to the canvas route. */
export function CanvasViewport() {
  const registerViewport = useContext(CanvasViewportContext)
  const sidebar = useSidebar()
  const sidebarRef = useRef(sidebar)
  sidebarRef.current = sidebar

  useEffect(() => {
    const previousOpen = sidebarRef.current.open
    sidebarRef.current.setOpen(false)
    sidebarRef.current.setOpenMobile(false)
    return () => sidebarRef.current.setOpen(previousOpen)
  }, [])

  return (
    <div ref={registerViewport} aria-hidden='true' className='min-h-0 flex-1' />
  )
}
