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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useForm } from 'react-hook-form'
import { beforeEach, describe, expect, it, vi } from 'vitest'

import { Form } from '@/components/ui/form'
import { api } from '@/lib/api'

import { ProbeTargetFields } from '../components/probe-target-fields'
import { defaultProbe } from '../lib/schema'
import type { ProbeConfig } from '../types'

function Fixture(props: { group?: boolean; disabled?: boolean }) {
  const form = useForm<ProbeConfig>({
    defaultValues: {
      ...defaultProbe,
      scope: props.group ? 'group' : 'upstream',
      group_name: props.group ? 'default' : '',
      channel_id: props.group ? 0 : 7,
      model: 'original',
      models: ['original'],
    },
  })
  return (
    <Form {...form}>
      <ProbeTargetFields disabled={props.disabled ?? false} />
      <output aria-label='Selected models'>
        {form.watch('models').join(',')}
      </output>
    </Form>
  )
}
function mount(group = false, disabled = false) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <Fixture group={group} disabled={disabled} />
    </QueryClientProvider>
  )
}
beforeEach(() => {
  vi.spyOn(api, 'get').mockImplementation(async (url, config) => {
    if (String(url).includes('/groups')) {
      return {
        data: {
          success: true,
          data: [
            { name: 'default', models: ['gpt-one', 'gpt-two'] },
            { name: 'other', models: ['gpt-three'] },
          ],
        },
      }
    }
    if (String(url).includes('/fetch_models/')) {
      return { data: { success: true, data: ['upstream-discovered'] } }
    }
    const next = config?.params?.p === 2
    return {
      data: {
        success: true,
        data: {
          items: next
            ? [{ id: 99, name: 'Beta', models: 'gpt-beta' }]
            : [{ id: 7, name: 'Alpha', models: 'gpt-alpha' }],
          total: 2,
        },
      },
    }
  })
})
describe('probe target and multiple model selection', () => {
  it('loads group models automatically, selects several and clears them when the group changes', async () => {
    mount(true)
    const user = userEvent.setup()
    const group = screen.getByRole('combobox', { name: 'Group' })
    await screen.findByText('1 selected · 2 available')
    await user.click(screen.getByRole('button', { name: 'Select all' }))
    expect(screen.getByLabelText('Selected models')).toHaveTextContent(
      'gpt-one,gpt-two'
    )
    await user.click(group)
    await user.clear(group)
    await user.click(screen.getByRole('option', { name: /^other$/ }))
    expect(screen.getByLabelText('Selected models')).toBeEmptyDOMElement()
    await user.click(screen.getByRole('button', { name: 'Select all' }))
    expect(screen.getByLabelText('Selected models')).toHaveTextContent(
      'gpt-three'
    )
  })
  it('includes upstream discovery results and finds channels past the first page', async () => {
    mount()
    const user = userEvent.setup()
    const channel = await screen.findByDisplayValue('Alpha (#7)')
    await screen.findByText('1 selected · 2 available')
    await user.click(screen.getByRole('button', { name: 'Select all' }))
    expect(screen.getByLabelText('Selected models')).toHaveTextContent(
      'gpt-alpha,upstream-discovered'
    )
    await user.click(channel)
    await user.clear(channel)
    await user.type(channel, '99')
    await user.click(screen.getByRole('option', { name: 'Beta (#99)' }))
    expect(screen.getByLabelText('Selected models')).toBeEmptyDOMElement()
    expect(channel).toHaveValue('Beta (#99)')
  })
  it('preserves edited selections if upstream discovery fails and disables actions during save', async () => {
    vi.mocked(api.get).mockRejectedValue(
      new Error('upstream discovery unavailable')
    )
    mount(false, true)
    expect(await screen.findByText('Failed to load channels')).toBeVisible()
    expect(screen.getByLabelText('Selected models')).toHaveTextContent(
      'original'
    )
    expect(screen.getByRole('combobox', { name: 'Channel' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Select all' })).toBeDisabled()
  })
})
