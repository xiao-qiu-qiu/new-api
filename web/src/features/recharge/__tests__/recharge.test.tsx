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
import { act, render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

import { Recharge } from '..'

describe('recharge store', () => {
  it('embeds the configured store and updates both entry points when the setting changes', async () => {
    const client = new QueryClient()
    client.setQueryData(['status'], {
      recharge_store_url: 'https://catfk.com/shop/40PFAHE3',
    })
    render(
      <QueryClientProvider client={client}>
        <Recharge />
      </QueryClientProvider>
    )
    expect(screen.getByTitle('Recharge Store')).toHaveAttribute(
      'src',
      'https://catfk.com/shop/40PFAHE3'
    )
    const link = screen.getByRole('link', { name: 'Open in new tab' })
    expect(link).toHaveAttribute('href', 'https://catfk.com/shop/40PFAHE3')
    expect(link).toHaveAttribute('target', '_blank')
    expect(link).toHaveAttribute('rel', 'noopener noreferrer')

    act(() => {
      client.setQueryData(['status'], {
        recharge_store_url: 'https://shop.example.com/recharge',
      })
    })
    await waitFor(() =>
      expect(screen.getByTitle('Recharge Store')).toHaveAttribute(
        'src',
        'https://shop.example.com/recharge'
      )
    )
    expect(link).toHaveAttribute('href', 'https://shop.example.com/recharge')
  })

  it.each(['', 'javascript:alert(1)'])(
    'shows the empty state for an unusable store URL (%s)',
    (url) => {
      const client = new QueryClient()
      client.setQueryData(['status'], { recharge_store_url: url })
      render(
        <QueryClientProvider client={client}>
          <Recharge />
        </QueryClientProvider>
      )
      expect(
        screen.getByText('Recharge store is not configured')
      ).toBeInTheDocument()
      expect(screen.queryByTitle('Recharge Store')).not.toBeInTheDocument()
      expect(
        screen.queryByRole('link', { name: 'Open in new tab' })
      ).not.toBeInTheDocument()
    }
  )
})
