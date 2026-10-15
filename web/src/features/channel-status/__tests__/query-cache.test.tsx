/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { QueryClientProvider, type QueryClient } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { createAppQueryClient } from '@/lib/query-client'
import { useAuthStore, type AuthUser } from '@/stores/auth-store'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { ChannelStatus } from '../index'
import type { PublicChannelStatus } from '../types'

const viewer: AuthUser = {
  id: 1,
  username: 'viewer',
  role: 1,
  group: 'default',
}

function statusResponse(price: number) {
  const probe: PublicChannelStatus = {
    public_name: 'Public group',
    scope: 'group',
    model: 'gpt-test',
    green_ms: 5000,
    yellow_ms: 10000,
    status: 'unknown',
    stale: false,
    latest: null,
    history: [],
    window_start: 1699996400,
    window_end: 1700000000,
    sample_count: 10,
    success_count: 10,
    timeline: [],
    estimated_price_per_100m_tokens: price,
    price_sample_count: 10,
  }
  return { data: { success: true, data: [probe] } }
}

let client: QueryClient
let response: Promise<ReturnType<typeof statusResponse>>

beforeEach(() => {
  window.localStorage.clear()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  client = createAppQueryClient()
  client.setQueryData(['status'], {})
  client.setQueryData(['notice'], { success: true, data: '' })
  response = Promise.resolve(statusResponse(12))
  vi.spyOn(window, 'scrollTo').mockImplementation(() => undefined)
  vi.spyOn(api, 'get').mockImplementation((url) => {
    if (url === '/api/channel/probes/status') return response
    throw new Error(`Unexpected GET ${url}`)
  })
})

afterEach(() => {
  cleanup()
  client.clear()
  useAuthStore.setState(useAuthStore.getInitialState(), true)
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  window.localStorage.clear()
})

async function renderStatus() {
  const router = createRouter({
    routeTree: createRootRoute({ component: ChannelStatus }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

describe('channel status price cache', () => {
  it.each([
    {
      change: 'switching accounts in the same group',
      from: viewer,
      to: { ...viewer, id: 2, username: 'other' },
    },
    {
      change: 'changing the current user group',
      from: viewer,
      to: { ...viewer, group: 'premium' },
    },
    { change: 'signing in', from: null, to: viewer },
    { change: 'signing out', from: viewer, to: null },
  ])(
    'hides the previous price while $change and loads the new price',
    async ({ from, to }) => {
      useAuthStore.getState().auth.setUser(from)
      await renderStatus()
      expect(await screen.findByText('≈$12')).toBeVisible()
      let resolveResponse!: (value: ReturnType<typeof statusResponse>) => void
      response = new Promise((resolve) => {
        resolveResponse = resolve
      })

      act(() => useAuthStore.getState().auth.setUser(to))

      expect(screen.queryByText('≈$12')).not.toBeInTheDocument()
      expect(screen.getByText('Loading model status...')).toBeVisible()
      resolveResponse(statusResponse(24))
      expect(await screen.findByText('≈$24')).toBeVisible()
    }
  )

  it('refreshes the same viewer price on remount despite the global fresh-cache window', async () => {
    useAuthStore.getState().auth.setUser(viewer)
    const view = await renderStatus()
    expect(await screen.findByText('≈$12')).toBeVisible()
    view.unmount()
    response = Promise.resolve(statusResponse(24))

    await renderStatus()

    expect(await screen.findByText('≈$24')).toBeVisible()
  })
})
