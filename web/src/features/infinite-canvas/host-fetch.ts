import { getFreshAuthHeaders, refreshAuthentication } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

const allowedPaths = /^\/pg\/(?:chat\/completions|responses|images\/(?:generations|edits)|audio\/speech|videos(?:\/[a-zA-Z0-9_-]+(?:\/content)?)?)$/

/** Keep authentication in the host; the editor receives responses, never a saved API key. */
export async function fetchCanvasRelay(
  userId: number,
  path: string,
  init: RequestInit = {}
): Promise<Response> {
  const url = new URL(path, window.location.origin)
  if (
    url.origin !== window.location.origin ||
    !allowedPaths.test(url.pathname) ||
    !['GET', 'POST'].includes((init.method || 'GET').toUpperCase())
  ) {
    throw new Error('Invalid canvas request')
  }
  const request = async () => {
    const authHeaders = await getFreshAuthHeaders()
    if (useAuthStore.getState().auth.user?.id !== userId) {
      throw new Error('Session expired!')
    }
    const headers = new Headers(init.headers)
    // FormData comes from another window, so instanceof is not reliable here.
    if (Object.prototype.toString.call(init.body) !== '[object FormData]') {
      if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json')
    } else {
      headers.delete('Content-Type')
    }
    headers.set('Authorization', authHeaders.Authorization || '')
    return fetch(url, { ...init, headers, credentials: 'same-origin', redirect: 'error', cache: 'no-store' })
  }
  const response = await request()
  if (response.status !== 401 || init.signal?.aborted) return response
  const refreshed = await refreshAuthentication()
  if (refreshed.kind !== 'authenticated') return response
  return request()
}
