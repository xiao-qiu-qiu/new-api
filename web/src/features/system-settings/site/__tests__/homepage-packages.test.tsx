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
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { createInstance } from 'i18next'
import { I18nextProvider } from 'react-i18next'
import { describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import type { UpdateOptionRequest } from '../../types'
import { HomepagePackagesSection } from '../homepage-packages-section'

const custom = {
  schemaVersion: 1,
  id: 'custom',
  name: 'My homepage',
  kind: 'html',
  layout: 'content',
  html: '<h1>Hello</h1>',
}

async function renderManager(active = 'codeq') {
  const language = createInstance()
  await language.init({
    lng: 'en',
    fallbackLng: 'en',
    resources: { en: { translation: {} } },
  })
  const client = new QueryClient({
    defaultOptions: { mutations: { retry: false } },
  })
  const route = createRootRoute({
    component: () => (
      <I18nextProvider i18n={language}>
        <QueryClientProvider client={client}>
          <HomepagePackagesSection
            defaultValue={JSON.stringify({
              schemaVersion: 1,
              active,
              packages: [custom],
            })}
            legacy=''
          />
        </QueryClientProvider>
      </I18nextProvider>
    ),
  })
  const router = createRouter({
    routeTree: route,
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await router.load()
  render(<RouterProvider router={router} />)
}

describe('Homepage package management', () => {
  it('keeps built-in packages read-only while allowing activation', async () => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    await renderManager()
    expect(screen.getByRole('textbox', { name: 'Package ID' })).toBeDisabled()
    expect(
      screen.queryByRole('button', { name: 'Delete' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Save and activate' }))
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    const request = put.mock.calls[0][1] as UpdateOptionRequest
    expect(put.mock.calls[0][0]).toBe('/api/option/')
    expect(request.key).toBe('HomePagePackages')
    expect(JSON.parse(String(request.value))).toEqual({
      schemaVersion: 1,
      active: 'official',
      packages: [custom],
    })
  })

  it('allows an empty draft field and rejects saving until it is valid', async () => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    await renderManager()
    await user.click(screen.getByRole('button', { name: 'My homepage' }))
    const name = screen.getByRole('textbox', { name: 'Name' })
    await user.clear(name)
    expect(name).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Save package' }))
    expect(put).not.toHaveBeenCalled()
    expect(await screen.findByRole('alert')).toHaveTextContent(
      'Invalid homepage package configuration'
    )
    await user.type(name, 'Updated homepage')
    await user.click(screen.getByRole('button', { name: 'Save package' }))
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    const payload = JSON.parse(
      String((put.mock.calls[0][1] as UpdateOptionRequest).value)
    )
    expect(payload.active).toBe('codeq')
    expect(payload.packages[0].name).toBe('Updated homepage')
  })

  it('retains the edited draft when storage rejects a save', async () => {
    const put = vi.spyOn(api, 'put').mockResolvedValue({
      data: { success: false, message: 'Storage unavailable' },
    })
    const user = userEvent.setup()
    await renderManager()
    await user.click(screen.getByRole('button', { name: 'My homepage' }))
    await user.type(screen.getByRole('textbox', { name: 'Name' }), ' edited')
    await user.click(screen.getByRole('button', { name: 'Save package' }))
    await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
    expect(screen.getByRole('textbox', { name: 'Name' })).toHaveValue(
      'My homepage edited'
    )
    expect(
      screen.getByRole('button', { name: 'New HTML package' })
    ).toBeDisabled()
  })

  it('restores the official homepage after confirming deletion of the active package', async () => {
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    const user = userEvent.setup()
    await renderManager('custom')
    await user.click(screen.getByRole('button', { name: /^My homepage/ }))
    await user.click(screen.getByRole('button', { name: 'Delete' }))
    expect(put).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Continue' }))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith('/api/option/', {
        key: 'HomePagePackages',
        value: JSON.stringify({
          schemaVersion: 1,
          active: 'official',
          packages: [],
        }),
      })
    )
  })
})
