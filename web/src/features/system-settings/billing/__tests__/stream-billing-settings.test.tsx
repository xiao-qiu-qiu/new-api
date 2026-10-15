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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { beforeEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { StreamBillingSection } from '../stream-billing-section'

async function renderSettings(mode: 'cancel' | 'drain' = 'cancel') {
  function Fixture() {
    const [container, setContainer] = useState<HTMLDivElement | null>(null)
    return (
      <>
        <div ref={setContainer} />
        <SettingsPageProvider actionsContainer={container}>
          <StreamBillingSection
            defaultValues={{
              quota_setting: {
                stream_disconnect_mode: mode,
                stream_drain_timeout_seconds: 300,
              },
            }}
          />
        </SettingsPageProvider>
      </>
    )
  }
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const router = createRouter({
    routeTree: createRootRoute({ component: Fixture }),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  render(
    <QueryClientProvider client={client}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  )
  return screen.findByRole('combobox', { name: 'Billing after disconnect' })
}

beforeEach(() => {
  vi.spyOn(api, 'put').mockResolvedValue({ data: { success: true } })
})

test('switching to usage collection enables its timeout and saves both settings', async () => {
  const user = userEvent.setup()
  const mode = await renderSettings()
  const timeout = screen.getByRole('spinbutton', {
    name: 'Collection timeout (seconds)',
  })
  expect(mode).toHaveTextContent('Disconnect immediately')
  expect(timeout).toBeDisabled()
  await user.click(mode)
  await user.click(
    await screen.findByRole('option', { name: 'Collect upstream usage' })
  )
  expect(timeout).toBeEnabled()
  fireEvent.change(timeout, { target: { value: '120' } })
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'quota_setting.stream_disconnect_mode',
      value: 'drain',
    })
  )
  expect(api.put).toHaveBeenCalledWith('/api/option/', {
    key: 'quota_setting.stream_drain_timeout_seconds',
    value: 120,
  })
})

test('a saved collection mode can be switched back using the keyboard', async () => {
  const user = userEvent.setup()
  const mode = await renderSettings('drain')
  expect(mode).toHaveTextContent('Collect upstream usage')
  mode.focus()
  await user.keyboard('{Enter}{Home}{Enter}')
  expect(mode).toHaveTextContent('Disconnect immediately')
  expect(
    screen.getByRole('spinbutton', { name: 'Collection timeout (seconds)' })
  ).toBeDisabled()
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() =>
    expect(api.put).toHaveBeenCalledWith('/api/option/', {
      key: 'quota_setting.stream_disconnect_mode',
      value: 'cancel',
    })
  )
})

test.each(['0', '3601', '1.5', ''])(
  'invalid collection timeout %s prevents saving',
  async (value) => {
    await renderSettings('drain')
    const timeout = screen.getByRole('spinbutton', {
      name: 'Collection timeout (seconds)',
    })
    fireEvent.change(timeout, { target: { value } })
    fireEvent.click(screen.getByRole('button', { name: 'Save Changes' }))
    await waitFor(() => expect(timeout).toHaveAttribute('aria-invalid', 'true'))
    expect(api.put).not.toHaveBeenCalled()
  }
)

test('a failed save preserves the selected mode for retry', async () => {
  const user = userEvent.setup()
  vi.mocked(api.put).mockResolvedValueOnce({
    data: { success: false, message: 'Save failed' },
  })
  const mode = await renderSettings()
  await user.click(mode)
  await user.click(
    await screen.findByRole('option', { name: 'Collect upstream usage' })
  )
  await user.click(screen.getByRole('button', { name: 'Save Changes' }))
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(1))
  const save = screen.getByRole('button', { name: 'Save Changes' })
  await waitFor(() => expect(save).toBeEnabled())
  expect(mode).toHaveTextContent('Collect upstream usage')
  await user.click(save)
  await waitFor(() => expect(api.put).toHaveBeenCalledTimes(2))
})
