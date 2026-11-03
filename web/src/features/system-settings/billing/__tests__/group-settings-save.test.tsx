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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { afterEach, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { RatioSettingsCard } from '../../models/ratio-settings-card'
import { groupSettingsJsonSchemas } from '../group-settings-schema'

const defaults = {
  UserGroups: '{"default":"standard","members":"standard","unused":"standard"}',
  GroupRatio: '{"standard":1,"premium":2}',
  TopupGroupRatio: '{"members":1.2,"unused":1.5}',
  UserUsableGroups: '{"standard":"Standard","auto":"Auto","legacy":"Legacy"}',
  GroupGroupRatio: '{"unused":{"standard":0.5},"members":{"premium":1.5}}',
  GroupSpecialUsableGroup:
    '{"unused":{"+:premium":"Premium"},"members":{"-:standard":"remove"}}',
  AutoGroups: '["standard"]',
  MaxTokenAutoGroups: 20,
  DefaultUseAutoGroup: false,
}
const modelDefaults = {
  ModelPrice: '{}',
  ModelRatio: '{}',
  CacheRatio: '{}',
  CreateCacheRatio: '{}',
  CompletionRatio: '{}',
  ImageRatio: '{}',
  AudioRatio: '{}',
  AudioCompletionRatio: '{}',
  ExposeRatioEnabled: false,
  BillingMode: '{}',
  BillingExpr: '{}',
  PluginBillingExpr: '{}',
}
const clients: QueryClient[] = []
afterEach(() => clients.splice(0).forEach((client) => client.clear()))

function renderSettings(initial: Partial<typeof defaults> = {}) {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  clients.push(client)
  function Fixture() {
    const [actions, setActions] = useState<HTMLDivElement | null>(null)
    return (
      <>
        <div ref={setActions} />
        <SettingsPageProvider actionsContainer={actions}>
          <RatioSettingsCard
            groupDefaults={{ ...defaults, ...initial }}
            modelDefaults={modelDefaults}
            toolPricesDefault='{}'
            visibleTabs={['groups']}
          />
        </SettingsPageProvider>
      </>
    )
  }
  render(
    <QueryClientProvider client={client}>
      <Fixture />
    </QueryClientProvider>
  )
  return client
}

test.each([
  ['UserGroups', 'null'],
  ['UserGroups', '[]'],
  ['UserGroups', '{"members":1}'],
  ['TopupGroupRatio', '{"members":-1}'],
  ['TopupGroupRatio', '{"members":""}'],
  ['GroupRatio', '{"standard":1e999}'],
  ['GroupRatio', '[]'],
  ['GroupGroupRatio', '{"members":null}'],
  ['GroupGroupRatio', '{"members":{"standard":"1"}}'],
  ['GroupSpecialUsableGroup', '{"members":[]}'],
  ['GroupSpecialUsableGroup', '{"members":{"standard":1}}'],
  ['UserUsableGroups', '{"standard":null}'],
  ['AutoGroups', 'null'],
])('invalid %s JSON %s cannot pass group form validation', (key, value) => {
  expect(
    groupSettingsJsonSchemas[
      key as keyof typeof groupSettingsJsonSchemas
    ].safeParse(JSON.parse(value)).success
  ).toBe(false)
})

test('zero and fractional numeric ratios remain valid', () => {
  expect(
    groupSettingsJsonSchemas.TopupGroupRatio.safeParse({ members: 0 }).success
  ).toBe(true)
  expect(
    groupSettingsJsonSchemas.GroupRatio.safeParse({ standard: 0 }).success
  ).toBe(true)
  expect(
    groupSettingsJsonSchemas.TopupGroupRatio.safeParse({ members: 0.0001 })
      .success
  ).toBe(true)
  expect(
    groupSettingsJsonSchemas.GroupGroupRatio.safeParse({
      members: { standard: 0 },
    }).success
  ).toBe(true)
})

test('deleting an unused user group saves all dependent removals atomically and invalidates group lists', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const client = renderSettings()
  const invalidate = vi.spyOn(client, 'invalidateQueries')
  await user.click(screen.getByRole('tab', { name: 'User groups' }))
  await user.click(
    screen.getByRole('button', { name: 'Delete user group unused' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  )
  await user.click(screen.getByRole('button', { name: 'Save group settings' }))
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  const [url, payload] = put.mock.calls[0]
  expect(url).toBe('/api/group/settings')
  expect(payload).toEqual({
    options: {
      UserGroups: '{"default":"standard","members":"standard"}',
      TopupGroupRatio: '{"members":1.2}',
      GroupGroupRatio: '{"members":{"premium":1.5}}',
      'group_ratio_setting.group_special_usable_group':
        '{"members":{"-:standard":"remove"}}',
    },
  })
  await waitFor(() =>
    expect(invalidate).toHaveBeenCalledWith({
      queryKey: ['account-user-groups'],
    })
  )
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ['groups'] })
  expect(invalidate).toHaveBeenCalledWith({ queryKey: ['user-groups'] })
})

test('a rejected save retains the draft and sends the same changes when retried', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValueOnce({
      data: { success: false, message: 'User group is still referenced' },
    })
    .mockResolvedValue({ data: { success: true } })
  renderSettings()
  await user.click(screen.getByRole('tab', { name: 'User groups' }))
  const ratio = screen.getByRole('spinbutton', {
    name: 'Top-up ratio for members',
  })
  fireEvent.change(ratio, { target: { value: '0.75' } })
  const save = screen.getByRole('button', { name: 'Save group settings' })
  await user.click(save)
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  await waitFor(() => expect(save).toBeEnabled())
  expect(ratio).toHaveValue(0.75)
  await user.click(save)
  await waitFor(() => expect(put).toHaveBeenCalledTimes(2))
  expect(put.mock.calls[1]).toEqual(put.mock.calls[0])
})

test.each(['-1'])(
  'a negative recharge draft %s blocks the save request',
  async (value) => {
    const user = userEvent.setup()
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    renderSettings()
    await user.click(screen.getByRole('tab', { name: 'User groups' }))
    fireEvent.change(
      screen.getByRole('spinbutton', { name: 'Top-up ratio for members' }),
      { target: { value } }
    )
    await user.click(
      screen.getByRole('button', { name: 'Save group settings' })
    )
    expect(
      await screen.findByText('JSON structure is invalid')
    ).toBeInTheDocument()
    expect(put).not.toHaveBeenCalled()
  }
)

test('a zero recharge ratio is saved as a number without removing its override', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  renderSettings()
  await user.click(screen.getByRole('tab', { name: 'User groups' }))
  fireEvent.change(
    screen.getByRole('spinbutton', { name: 'Top-up ratio for members' }),
    { target: { value: '0' } }
  )
  await user.click(screen.getByRole('button', { name: 'Save group settings' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/group/settings', {
      options: { TopupGroupRatio: '{"members":0,"unused":1.5}' },
    })
  )
})

test('editing an override excludes occupied targets and preserves the other rule when saved', async () => {
  const user = userEvent.setup()
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  renderSettings({
    GroupGroupRatio: '{"members":{"standard":0.5,"premium":0.8}}',
  })
  await user.click(screen.getByRole('tab', { name: 'Special ratio rules' }))
  const premiumRow = screen.getByRole('row', { name: /premium/ })
  await user.click(within(premiumRow).getByRole('button', { name: 'Edit' }))
  const dialog = screen.getByRole('dialog', { name: 'Edit ratio override' })
  await user.click(
    within(dialog).getByRole('combobox', { name: 'Select a multiplier group' })
  )
  expect(
    screen.queryByRole('option', { name: 'standard' })
  ).not.toBeInTheDocument()
  await user.click(screen.getByRole('option', { name: 'premium' }))
  fireEvent.change(within(dialog).getByRole('textbox'), {
    target: { value: '0.7' },
  })
  await user.click(within(dialog).getByRole('button', { name: 'Update' }))
  await user.click(screen.getByRole('button', { name: 'Save group settings' }))
  await waitFor(() =>
    expect(put).toHaveBeenCalledWith('/api/group/settings', {
      options: {
        GroupGroupRatio: '{"members":{"standard":0.5,"premium":0.7}}',
      },
    })
  )
})

test('adding an override excludes only targets occupied by the selected user group', async () => {
  const user = userEvent.setup()
  renderSettings({
    GroupGroupRatio: '{"members":{"premium":0.8},"unused":{"standard":0.5}}',
  })
  await user.click(screen.getByRole('tab', { name: 'Special ratio rules' }))
  await user.click(
    screen.getAllByRole('button', { name: 'Add ratio override' })[0]
  )
  const dialog = screen.getByRole('dialog', { name: 'Add ratio override' })
  await user.click(
    within(dialog).getByRole('combobox', { name: 'Select a multiplier group' })
  )
  expect(
    screen.queryByRole('option', { name: 'premium' })
  ).not.toBeInTheDocument()
  expect(screen.getByRole('option', { name: 'standard' })).toBeInTheDocument()
})
