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
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { cleanup, render, screen } from '@testing-library/react'
import type { ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, it } from 'vitest'

import { STATUS_QUERY_KEY } from '@/lib/status-query'
import { useSystemConfigStore } from '@/stores/system-config-store'

import { Footer } from '../footer'

let client: QueryClient

beforeEach(() => {
  window.localStorage.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  useSystemConfigStore.getState().setConfig({
    systemName: 'Footer test site',
    footerHtml: '',
    demoSiteEnabled: false,
  })
  client = new QueryClient({
    defaultOptions: { queries: { enabled: false, retry: false } },
  })
  client.setQueryData(STATUS_QUERY_KEY, {})
})

afterEach(() => {
  cleanup()
  client.clear()
  useSystemConfigStore.setState(useSystemConfigStore.getInitialState(), true)
  window.localStorage.clear()
})

async function renderFooter(props: ComponentProps<typeof Footer> = {}) {
  const router = createRouter({
    routeTree: createRootRoute({ component: () => <Footer {...props} /> }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  return render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
}

describe('Footer tagline', () => {
  it('shows the tagline by default', async () => {
    await renderFooter()

    expect(screen.getByText('Powerful API Management Platform')).toBeVisible()
  })

  it('omits the tagline paragraph when false and retains copyright and attribution', async () => {
    await renderFooter({ showTagline: false })

    expect(
      screen.queryByText('Powerful API Management Platform')
    ).not.toBeInTheDocument()
    expect(screen.queryByRole('paragraph')).not.toBeInTheDocument()
    expect(
      screen.getByText(
        `© ${new Date().getFullYear()} Footer test site. footer.defaultCopyright`
      )
    ).toBeVisible()
    expect(screen.getByRole('link', { name: 'New API' })).toHaveAttribute(
      'href',
      'https://github.com/QuantumNous/new-api'
    )
    expect(
      screen.getByText(/footer\.newapi\.projectAttributionSuffix/)
    ).toBeVisible()
  })
})
