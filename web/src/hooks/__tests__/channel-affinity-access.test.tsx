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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from '@tanstack/react-router'
import { act, cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { ROLE } from '@/lib/roles'
import { Route as AffinityRoute } from '@/routes/_authenticated/channel-affinity/index'
import { useAuthStore, type AuthUser } from '@/stores/auth-store'

import { useSidebarView } from '../use-sidebar-view'

let queryClient: QueryClient

afterEach(() => {
  cleanup()
  queryClient?.clear()
  useAuthStore.getState().auth.reset()
})

function NavigationFixture() {
  const { navGroups } = useSidebarView()
  return (
    <>
      <nav aria-label='Console'>
        {navGroups
          .flatMap((group) => group.items)
          .map((item) =>
            item.url ? (
              <a key={item.url} href={item.url}>
                {item.title}
              </a>
            ) : null
          )}
      </nav>
      <Outlet />
    </>
  )
}

async function openAffinityPage(user: AuthUser | null) {
  useAuthStore.getState().auth.setUser(user)
  queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  queryClient.setQueryData(['status'], { SidebarModulesAdmin: '' })

  const root = createRootRoute({ component: NavigationFixture })
  const affinity = createRoute({
    getParentRoute: () => root,
    path: '/channel-affinity',
    beforeLoad: AffinityRoute.options.beforeLoad as () => void,
    component: () => <h1>Affinity sessions panel</h1>,
  })
  const forbidden = createRoute({
    getParentRoute: () => root,
    path: '/403',
    component: () => <h1>Forbidden</h1>,
  })
  const router = createRouter({
    routeTree: root.addChildren([affinity, forbidden]),
    history: createMemoryHistory({ initialEntries: ['/channel-affinity'] }),
  })
  render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  await act(() => router.load())
  return router
}

describe('affinity sessions navigation access', () => {
  it.each([
    ['ordinary user with a read grant', ROLE.USER, { read: true }],
    ['administrator without read', ROLE.ADMIN, { read: false, operate: true }],
    ['administrator with no permission matrix', ROLE.ADMIN, undefined],
  ])(
    '%s has no sidebar entry and a direct URL redirects to 403',
    async (_name, role, channel) => {
      const router = await openAffinityPage({
        id: 2,
        username: 'viewer',
        role,
        permissions: { admin_permissions: channel ? { channel } : undefined },
      })

      expect(
        await screen.findByRole('heading', { name: 'Forbidden' })
      ).toBeVisible()
      expect(router.state.location.pathname).toBe('/403')
      expect(
        screen.queryByRole('link', { name: 'Affinity sessions' })
      ).not.toBeInTheDocument()
      expect(
        screen.queryByRole('heading', { name: 'Affinity sessions panel' })
      ).not.toBeInTheDocument()
    }
  )

  it('an administrator with read but without operate sees the entry and can open the page', async () => {
    await openAffinityPage({
      id: 2,
      username: 'viewer',
      role: ROLE.ADMIN,
      permissions: {
        admin_permissions: { channel: { read: true, operate: false } },
      },
    })

    expect(
      await screen.findByRole('heading', { name: 'Affinity sessions panel' })
    ).toBeVisible()
    expect(
      screen.getByRole('link', { name: 'Affinity sessions' })
    ).toHaveAttribute('href', '/channel-affinity')
  })

  it('a super administrator uses the existing superuser permission grant', async () => {
    await openAffinityPage({ id: 1, username: 'root', role: ROLE.SUPER_ADMIN })
    expect(
      await screen.findByRole('heading', { name: 'Affinity sessions panel' })
    ).toBeVisible()
    expect(
      screen.getByRole('link', { name: 'Affinity sessions' })
    ).toBeVisible()
  })

  it('an absent user has no sidebar entry and direct access is rejected', async () => {
    await openAffinityPage(null)
    expect(
      await screen.findByRole('heading', { name: 'Forbidden' })
    ).toBeVisible()
    expect(
      screen.queryByRole('link', { name: 'Affinity sessions' })
    ).not.toBeInTheDocument()
  })

  it('revoking channel.read removes an already visible entry', async () => {
    const user: AuthUser = {
      id: 2,
      username: 'viewer',
      role: ROLE.ADMIN,
      permissions: { admin_permissions: { channel: { read: true } } },
    }
    await openAffinityPage(user)
    expect(
      await screen.findByRole('link', { name: 'Affinity sessions' })
    ).toBeVisible()

    act(() =>
      useAuthStore.getState().auth.setUser({
        ...user,
        permissions: { admin_permissions: { channel: { read: false } } },
      })
    )

    expect(
      screen.queryByRole('link', { name: 'Affinity sessions' })
    ).not.toBeInTheDocument()
  })
})
