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
import {
  act,
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test, vi } from 'vitest'

import type { ApiKey } from '../../types'

const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { QueryClient, QueryClientProvider } =
  await import('@tanstack/react-query')
const { api } = await import('@/lib/api')
const { ApiKeysProvider } = await import('../api-keys-provider')
const { ApiKeysMutateDrawer } = await import('../api-keys-mutate-drawer')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

type ApiMethod = (url: string, data?: unknown) => Promise<{ data: unknown }>
type MockableApi = {
  get: ApiMethod
  post: ApiMethod
}
type RenderedDrawer = {
  queryClient: InstanceType<typeof QueryClient>
}

const apiClient = api as unknown as MockableApi
const originalGet = apiClient.get
const originalPost = apiClient.post
let renderedDrawer: RenderedDrawer | null = null

function installApiFixtures(
  createdPayloads: Array<Record<string, unknown>>,
  models: string[] = [],
  groupModels: Record<string, string[]> = {},
  existingKey?: ApiKey
) {
  apiClient.get = async (url) => {
    if (existingKey && url === `/api/token/${existingKey.id}`) {
      return { data: { success: true, data: existingKey } }
    }
    if (url.startsWith('/api/user/models?group=')) {
      const group =
        new URL(url, 'http://localhost').searchParams.get('group') || ''
      return { data: { success: true, data: groupModels[group] ?? models } }
    }
    switch (url) {
      case '/api/user/models':
        return { data: { success: true, data: models } }
      case '/api/user/self/groups':
        return {
          data: {
            success: true,
            data: {
              default: { desc: 'Standard access', ratio: 1 },
              vip: { desc: 'Priority access', ratio: 2 },
            },
          },
        }
      case '/api/token/auto-groups':
        return {
          data: {
            success: true,
            data: { groups: ['vip', 'default'], max_count: 3 },
          },
        }
      default:
        throw new Error(`Unexpected GET ${url}`)
    }
  }
  apiClient.post = async (url, data) => {
    expect(url).toBe('/api/token/')
    expect(data && typeof data === 'object').toBeTruthy()
    createdPayloads.push(data as Record<string, unknown>)
    return { data: { success: true, data: {} } }
  }
}

async function renderDrawer(
  models: string[] = [],
  currentRow?: ApiKey
): Promise<void> {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(
    ['user-models'],
    { success: true, data: models },
    { updatedAt: freshAt }
  )
  queryClient.setQueryData(
    ['user-groups'],
    {
      success: true,
      data: {
        default: { desc: 'Standard access', ratio: 1 },
        vip: { desc: 'Priority access', ratio: 2 },
      },
    },
    { updatedAt: freshAt }
  )
  queryClient.setQueryData(
    ['token-auto-groups'],
    {
      success: true,
      data: { groups: ['vip', 'default'], max_count: 3 },
    },
    { updatedAt: freshAt }
  )
  renderedDrawer = { queryClient }

  render(
    <QueryClientProvider client={queryClient}>
      <I18nextProvider i18n={i18n}>
        <ApiKeysProvider>
          <ApiKeysMutateDrawer
            open
            currentRow={currentRow}
            onOpenChange={() => undefined}
          />
        </ApiKeysProvider>
      </I18nextProvider>
    </QueryClientProvider>
  )
  await waitFor(
    () => {
      const saveButton = findButton('Save changes', false)
      expect(saveButton).toBeEnabled()
    },
    { timeout: 1500 }
  )
}

function findButton(text: string, required: true): HTMLButtonElement
function findButton(text: string, required: false): HTMLButtonElement | null
function findButton(text: string, required = true): HTMLButtonElement | null {
  const button = screen
    .queryAllByRole<HTMLButtonElement>('button')
    .find((candidate) => candidate.textContent?.includes(text))
  if (required && !button) {
    throw new Error(`Expected button containing "${text}"`)
  }
  return button ?? null
}

function getControlByLabel(labelText: 'Name' | 'Quantity'): HTMLInputElement
function getControlByLabel(labelText: 'Model Limits'): HTMLButtonElement
function getControlByLabel(labelText: 'Selected groups'): HTMLElement
function getControlByLabel(labelText: string): HTMLElement {
  const label = [...document.querySelectorAll<HTMLLabelElement>('label')].find(
    (candidate) => candidate.textContent?.trim() === labelText
  )
  if (!label) {
    throw new Error(`Expected label "${labelText}"`)
  }

  const control =
    label.control ??
    label
      .closest('[data-slot="form-item"]')
      ?.querySelector<HTMLElement>(
        '[data-slot="form-control"], input, textarea, button[role="combobox"], [role="group"]'
      )
  if (!control) {
    throw new Error(`Expected control for label "${labelText}"`)
  }
  return control
}

function changeInput(input: HTMLInputElement, value: string): void {
  fireEvent.input(input, { target: { value } })
}

function selectComboboxOption(
  trigger: HTMLButtonElement,
  optionDescription: string
): void {
  fireEvent.click(trigger)
  const option = [
    ...document.querySelectorAll<HTMLElement>('[data-slot="command-item"]'),
  ].find((candidate) => candidate.textContent?.includes(optionDescription))
  if (!option) {
    throw new Error(`Expected option containing "${optionDescription}"`)
  }
  fireEvent.click(option)
}

afterEach(() => {
  apiClient.get = originalGet
  apiClient.post = originalPost
  localStorage.clear()
  if (renderedDrawer) {
    renderedDrawer.queryClient.clear()
    renderedDrawer = null
  }
})

const storedKey: ApiKey = {
  id: 3,
  name: 'existing',
  key: '',
  status: 1,
  remain_quota: 0,
  used_quota: 0,
  unlimited_quota: true,
  expired_time: -1,
  created_time: 1,
  accessed_time: 0,
  group: 'default',
  auto_groups: [],
  cross_group_retry: false,
  model_limits_enabled: false,
  model_limits: '',
  allow_ips: '',
  allow_user_quota_query: true,
}

function addGroup(description: string) {
  selectComboboxOption(
    within(getControlByLabel('Selected groups')).getByRole<HTMLButtonElement>(
      'combobox'
    ),
    description
  )
}

describe('API keys mutate drawer explicit group integration', () => {
  test('starts empty without an Auto entry and blocks saving until a group is selected', async () => {
    const payloads: Array<Record<string, unknown>> = []
    installApiFixtures(payloads)
    await renderDrawer()
    expect(screen.getByText('0 / 3 groups selected')).toBeVisible()
    expect(
      screen.queryByRole('button', { name: 'Single group' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Multiple groups' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('button', { name: 'Use global groups' })
    ).not.toBeInTheDocument()
    expect(
      screen.queryByRole('switch', { name: 'Cross-group retry' })
    ).not.toBeInTheDocument()
    changeInput(getControlByLabel('Name'), 'explicit')
    fireEvent.click(findButton('Save changes', true))
    expect(await screen.findByText('Select at least one group')).toBeVisible()
    expect(getControlByLabel('Selected groups')).toHaveAttribute(
      'aria-invalid',
      'true'
    )
    expect(payloads).toHaveLength(0)
    addGroup('Standard access')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(payloads).toHaveLength(1))
    expect(payloads[0]).toMatchObject({
      group: 'default',
      auto_groups: [],
      cross_group_retry: false,
    })
  })

  test('saves a single explicit group for every batch-created key', async () => {
    const payloads: Array<Record<string, unknown>> = []
    installApiFixtures(payloads)
    await renderDrawer()
    addGroup('Priority access')
    expect(
      screen.queryByRole('switch', { name: 'Cross-group retry' })
    ).not.toBeInTheDocument()
    changeInput(getControlByLabel('Name'), 'batch')
    changeInput(getControlByLabel('Quantity'), '2')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(payloads).toHaveLength(2))
    expect(payloads[0]?.name).toBe('batch')
    for (const payload of payloads) {
      expect(payload).toMatchObject({
        group: 'vip',
        auto_groups: [],
        cross_group_retry: false,
      })
    }
  })

  test('saves multiple groups in the edited order with explicit retry selection', async () => {
    const payloads: Array<Record<string, unknown>> = []
    installApiFixtures(payloads)
    await renderDrawer()
    addGroup('Standard access')
    addGroup('Priority access')
    const retry = screen.getByRole('switch', { name: 'Cross-group retry' })
    expect(retry).toBeChecked()
    fireEvent.click(retry)
    fireEvent.click(screen.getByRole('button', { name: 'Move vip up' }))
    changeInput(getControlByLabel('Name'), 'ordered')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(payloads).toHaveLength(1))
    expect(payloads[0]).toMatchObject({
      group: 'auto',
      auto_groups: ['vip', 'default'],
      cross_group_retry: false,
    })
  })

  test('hides cross-group retry again when the selection is reduced to one group', async () => {
    const payloads: Array<Record<string, unknown>> = []
    installApiFixtures(payloads)
    await renderDrawer()
    addGroup('Standard access')
    addGroup('Priority access')
    expect(
      screen.getByRole('switch', { name: 'Cross-group retry' })
    ).toBeChecked()
    fireEvent.click(screen.getByRole('button', { name: 'Remove default' }))
    expect(
      screen.queryByRole('switch', { name: 'Cross-group retry' })
    ).not.toBeInTheDocument()
    changeInput(getControlByLabel('Name'), 'single')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(payloads).toHaveLength(1))
    expect(payloads[0]).toMatchObject({
      group: 'vip',
      auto_groups: [],
      cross_group_retry: false,
    })
  })

  test.each([
    { group: 'vip', auto_groups: [], selected: ['vip'] },
    {
      group: 'auto',
      auto_groups: ['vip', 'default'],
      selected: ['vip', 'default'],
    },
  ])(
    'preserves an existing $group key when saved without group edits',
    async ({ group, auto_groups, selected }) => {
      const currentRow = {
        ...storedKey,
        group,
        auto_groups,
        cross_group_retry: true,
      }
      installApiFixtures([], [], {}, currentRow)
      const put = vi
        .spyOn(api, 'put')
        .mockResolvedValue({ data: { success: true } })
      await renderDrawer([], currentRow)
      for (const selectedGroup of selected) {
        expect(
          screen.getByRole('button', { name: `Remove ${selectedGroup}` })
        ).toBeVisible()
      }
      fireEvent.click(findButton('Save changes', true))
      await waitFor(() =>
        expect(put).toHaveBeenCalledWith(
          '/api/token/',
          expect.objectContaining({
            id: currentRow.id,
            group,
            auto_groups,
            cross_group_retry: selected.length > 1,
          })
        )
      )
    }
  )

  test('retains an unavailable legacy group and rejects saving until it is removed', async () => {
    const currentRow = {
      ...storedKey,
      group: 'auto',
      auto_groups: ['revoked', 'vip'],
    }
    installApiFixtures([], [], {}, currentRow)
    const put = vi
      .spyOn(api, 'put')
      .mockResolvedValue({ data: { success: true } })
    await renderDrawer([], currentRow)
    expect(screen.getByText('Unavailable')).toBeVisible()
    expect(screen.getByRole('button', { name: 'Remove revoked' })).toBeVisible()
    fireEvent.click(findButton('Save changes', true))
    expect(
      await screen.findByText(
        'Some selected groups are unavailable. Choose available groups.'
      )
    ).toBeVisible()
    expect(put).not.toHaveBeenCalled()
    fireEvent.click(screen.getByRole('button', { name: 'Remove revoked' }))
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() =>
      expect(put).toHaveBeenCalledWith(
        '/api/token/',
        expect.objectContaining({
          group: 'vip',
          auto_groups: [],
          cross_group_retry: false,
        })
      )
    )
  })
})

describe('API key expiry across editing sessions', () => {
  test.each(['close', 'save'] as const)(
    'keeps an uncached key non-expiring after %s of a key with an expiry',
    async (finish) => {
      installApiFixtures([])
      const first: ApiKey = {
        id: 1,
        name: 'expiring',
        key: '',
        status: 1,
        remain_quota: 0,
        used_quota: 0,
        unlimited_quota: true,
        expired_time: 2000000000,
        created_time: 1,
        accessed_time: 0,
        group: 'default',
        auto_groups: [],
        cross_group_retry: false,
        model_limits_enabled: false,
        model_limits: '',
        allow_ips: '',
        allow_user_quota_query: true,
      }
      const second = { ...first, id: 2, name: 'non-expiring', expired_time: -1 }
      let resolveDetail!: (value: { data: unknown }) => void
      const detail = new Promise<{ data: unknown }>((resolve) => {
        resolveDetail = resolve
      })
      const getFixture = apiClient.get
      const get = vi.spyOn(apiClient, 'get').mockImplementation((url) => {
        if (url === '/api/token/1') {
          return Promise.resolve({ data: { success: true, data: first } })
        }
        if (url === '/api/token/2') return detail
        return getFixture(url)
      })
      const put = vi
        .spyOn(api, 'put')
        .mockResolvedValue({ data: { success: true } })
      const queryClient = new QueryClient({
        defaultOptions: { queries: { retry: false } },
      })
      renderedDrawer = { queryClient }
      const close = vi.fn()
      const tree = (currentRow: ApiKey, open: boolean) => (
        <QueryClientProvider client={queryClient}>
          <I18nextProvider i18n={i18n}>
            <ApiKeysProvider>
              <ApiKeysMutateDrawer
                open={open}
                currentRow={currentRow}
                onOpenChange={close}
              />
            </ApiKeysProvider>
          </I18nextProvider>
        </QueryClientProvider>
      )
      const view = render(tree(first, true))
      await waitFor(() =>
        expect(findButton('Save changes', true)).toBeEnabled()
      )
      await waitFor(() => expect(screen.getByLabelText('Time')).toBeEnabled())

      if (finish === 'save') {
        fireEvent.click(findButton('Save changes', true))
        await waitFor(() => expect(put).toHaveBeenCalled())
        expect(put.mock.calls[0][1]).toMatchObject({
          id: 1,
          expired_time: 2000000000,
        })
      } else {
        fireEvent.click(screen.getAllByRole('button', { name: 'Close' })[0])
      }
      await waitFor(() => expect(close.mock.calls[0]?.[0]).toBe(false))
      view.rerender(tree(first, false))
      await waitFor(() =>
        expect(screen.queryByRole('dialog')).not.toBeInTheDocument()
      )
      view.rerender(tree(second, true))
      await waitFor(() => expect(get).toHaveBeenCalledWith('/api/token/2'))
      expect(findButton('Save changes', true)).toBeDisabled()
      await act(async () =>
        resolveDetail({ data: { success: true, data: second } })
      )
      await waitFor(() =>
        expect(findButton('Save changes', true)).toBeEnabled()
      )
      expect(
        screen.getByRole('button', { name: 'Never expires' })
      ).toBeVisible()
      expect(screen.getByLabelText('Time')).toBeDisabled()
      fireEvent.click(findButton('Save changes', true))
      await waitFor(() =>
        expect(put).toHaveBeenLastCalledWith(
          '/api/token/',
          expect.objectContaining({ id: 2, expired_time: -1 })
        )
      )
    }
  )

  test.each(['Never', 'Clear'])(
    'submits a non-expiring key when its date is cleared with %s',
    async (action) => {
      const payloads: Array<Record<string, unknown>> = []
      installApiFixtures(payloads)
      await renderDrawer()
      addGroup('Standard access')
      changeInput(getControlByLabel('Name'), 'no-expiry')
      fireEvent.click(screen.getByRole('button', { name: '1 Month' }))
      expect(screen.getByLabelText('Time')).toBeEnabled()
      fireEvent.click(screen.getByRole('button', { name: action }))
      expect(
        screen.getByRole('button', { name: 'Never expires' })
      ).toBeVisible()
      expect(screen.getByLabelText('Time')).toBeDisabled()
      fireEvent.click(findButton('Save changes', true))
      await waitFor(() => expect(payloads).toHaveLength(1))
      expect(payloads[0]?.expired_time).toBe(-1)
    }
  )
})

describe('API keys mutate drawer user quota query switch', () => {
  test('sends false for a new key after the switch, which starts on, is turned off', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderDrawer()

    fireEvent.click(findButton('Advanced Settings', true))
    const quotaQuerySwitch = await screen.findByRole('switch', {
      name: 'Allow querying user quota',
    })
    expect(quotaQuerySwitch).toBeChecked()
    fireEvent.click(quotaQuerySwitch)
    expect(quotaQuerySwitch).not.toBeChecked()

    addGroup('Standard access')
    changeInput(getControlByLabel('Name'), 'no-user-quota')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(1))
    expect(createdPayloads[0]?.allow_user_quota_query).toBe(false)
  })
})

describe('API keys mutate drawer GPT speed tier', () => {
  test('hides the tier control when only non-GPT or media models are available', async () => {
    const models = [
      'deepseek-chat',
      'claude-sonnet',
      'gpt-image-1',
      'gpt-4o-audio-preview',
    ]
    installApiFixtures([], models)
    await renderDrawer(models)
    addGroup('Standard access')

    expect(
      screen.queryByRole('radiogroup', { name: 'GPT speed tier' })
    ).not.toBeInTheDocument()
  })

  test('allows keyboard selection and sends the locked tier for every batch-created key', async () => {
    const user = userEvent.setup()
    const payloads: Array<Record<string, unknown>> = []
    const models = ['openai/gpt-6-astra', 'deepseek-chat']
    installApiFixtures(payloads, models)
    await renderDrawer(models)

    addGroup('Priority access')
    const follow = await screen.findByRole('radio', { name: 'Follow request' })
    expect(follow).toBeChecked()
    follow.focus()
    await user.keyboard('{ArrowRight}')
    expect(screen.getByRole('radio', { name: 'Lock Fast' })).toBeChecked()

    changeInput(getControlByLabel('Name'), 'fast-batch')
    changeInput(getControlByLabel('Quantity'), '2')
    await user.click(findButton('Save changes', true))
    await waitFor(() => expect(payloads).toHaveLength(2))
    expect(payloads.map((payload) => payload.openai_tier_policy)).toEqual([
      'priority',
      'priority',
    ])
  })

  test('hides the tier for non-GPT model limits and restores the choice when the limit is removed', async () => {
    const user = userEvent.setup()
    const models = ['gpt-6-astra', 'deepseek-chat']
    installApiFixtures([], models)
    await renderDrawer(models)
    addGroup('Priority access')
    await user.click(await screen.findByRole('radio', { name: 'Lock UF' }))
    expect(screen.getByRole('radio', { name: 'Lock UF' })).toBeChecked()

    await user.click(findButton('Advanced Settings', true))
    await user.click(
      screen.getByRole('combobox', {
        name: 'Select models (empty for allow all)',
      })
    )
    await user.click(
      await screen.findByRole('option', { name: 'deepseek-chat' })
    )
    expect(
      screen.queryByRole('radiogroup', { name: 'GPT speed tier' })
    ).not.toBeInTheDocument()

    const modelOption = screen.getByRole('option', { name: /deepseek-chat/ })
    await user.click(modelOption)
    expect(screen.getByRole('radio', { name: 'Lock UF' })).toBeChecked()
  })

  test('hides the tier after switching from a GPT group to a non-GPT group', async () => {
    const models = ['gpt-6-astra', 'deepseek-chat']
    installApiFixtures([], models, {
      default: ['deepseek-chat'],
      vip: ['gpt-6-astra'],
    })
    await renderDrawer(models)
    addGroup('Priority access')
    expect(
      await screen.findByRole('radiogroup', { name: 'GPT speed tier' })
    ).toBeVisible()

    fireEvent.click(screen.getByRole('button', { name: 'Remove vip' }))
    addGroup('Standard access')
    await waitFor(() =>
      expect(
        screen.queryByRole('radiogroup', { name: 'GPT speed tier' })
      ).not.toBeInTheDocument()
    )
  })
})
