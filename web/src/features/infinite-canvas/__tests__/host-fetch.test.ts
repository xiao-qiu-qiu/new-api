import { beforeEach, expect, it, vi } from 'vitest'

import { getFreshAuthHeaders, refreshAuthentication } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { fetchCanvasRelay } from '../host-fetch'

vi.mock('i18next', () => ({ t: (key: string) => key }))

vi.mock('@/lib/api', () => ({
  getFreshAuthHeaders: vi.fn(),
  refreshAuthentication: vi.fn(),
}))
vi.mock('@/stores/auth-store', () => ({ useAuthStore: { getState: vi.fn() } }))

beforeEach(() => {
  vi.clearAllMocks()
  vi.mocked(getFreshAuthHeaders).mockResolvedValue({
    Authorization: 'Bearer fixture',
  })
  vi.mocked(useAuthStore.getState).mockReturnValue({
    auth: { user: { id: 42 } },
  } as ReturnType<typeof useAuthStore.getState>)
})

it('rejects external URLs and non-relay paths before obtaining credentials', async () => {
  await expect(
    fetchCanvasRelay(42, 'https://example.com/pg/videos')
  ).rejects.toThrow('Request failed')
  await expect(fetchCanvasRelay(42, '/api/user/self')).rejects.toThrow(
    'Request failed'
  )
  expect(getFreshAuthHeaders).not.toHaveBeenCalled()
})

it('rejects a pending editor request when the signed-in account changes', async () => {
  await expect(fetchCanvasRelay(99, '/pg/videos')).rejects.toThrow(
    'Session expired!'
  )
})

it('lets the browser set a multipart boundary and keeps the cancellation signal', async () => {
  const request = vi
    .spyOn(globalThis, 'fetch')
    .mockResolvedValue(new Response('{}'))
  const body = new FormData()
  body.set('model', 'fixture-image')
  const signal = new AbortController().signal
  await fetchCanvasRelay(42, '/pg/images/edits', {
    method: 'POST',
    body,
    signal,
    headers: { 'Content-Type': 'application/json' },
  })
  const init = request.mock.calls[0][1]
  expect(new Headers(init?.headers).has('Content-Type')).toBe(false)
  expect(new Headers(init?.headers).get('Authorization')).toBe('Bearer fixture')
  expect(init?.signal).toBe(signal)
  expect(init?.redirect).toBe('error')
  request.mockRestore()
})

it('refreshes once after a rejected session without retrying provider failures', async () => {
  const request = vi
    .spyOn(globalThis, 'fetch')
    .mockResolvedValueOnce(new Response('{}', { status: 401 }))
    .mockResolvedValueOnce(new Response('{}', { status: 503 }))
  vi.mocked(refreshAuthentication).mockResolvedValue({
    kind: 'authenticated',
  } as Awaited<ReturnType<typeof refreshAuthentication>>)
  expect(
    (
      await fetchCanvasRelay(42, '/pg/images/generations', {
        method: 'POST',
        body: '{}',
      })
    ).status
  ).toBe(503)
  expect(refreshAuthentication).toHaveBeenCalledTimes(1)
  expect(request).toHaveBeenCalledTimes(2)
  request.mockRestore()
})
