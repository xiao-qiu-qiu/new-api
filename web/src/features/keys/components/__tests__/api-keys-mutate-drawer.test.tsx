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
  fireEvent,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, test } from 'vitest'

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
  groupModels: Record<string, string[]> = {}
) {
  apiClient.get = async (url) => {
    if (url.startsWith('/api/user/models?group=')) {
      const group =
        new URL(url, 'http://localhost').searchParams.get('group') || ''
      return { data: { success: true, data: groupModels[group] ?? models } }
    }
    switch (url) {
      case '/api/status':
        return { data: { data: { default_use_auto_group: true } } }
      case '/api/user/models':
        return { data: { success: true, data: models } }
      case '/api/user/self/groups':
        return {
          data: {
            success: true,
            data: {
              auto: { desc: 'Automatic routing', ratio: 'auto' },
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

async function renderCreateDrawer(
  defaultUseAutoGroup = true,
  models: string[] = []
): Promise<void> {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const freshAt = Date.now() + 60_000
  queryClient.setQueryData(
    ['status'],
    { default_use_auto_group: defaultUseAutoGroup },
    { updatedAt: freshAt }
  )
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
        auto: { desc: 'Automatic routing', ratio: 'auto' },
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
          <ApiKeysMutateDrawer open onOpenChange={() => undefined} />
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
function getControlByLabel(
  labelText: 'Group' | 'Model Limits'
): HTMLButtonElement
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

describe('API keys mutate drawer Auto group integration', () => {
  test('inherits the root Auto order and sends an empty override for every batch-created key', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderCreateDrawer()

    expect(
      screen.getByRole('button', { name: 'Multiple groups' })
    ).toHaveAttribute('aria-pressed', 'true')
    expect(
      document.body.textContent?.includes('Using global groups (2 groups)')
    ).toBe(true)
    expect(
      [
        ...document.querySelectorAll('[data-slot="global-auto-order-name"]'),
      ].map((item) => item.textContent)
    ).toEqual(['vip', 'default'])
    expect(findButton('Use global groups', true).disabled).toBe(true)

    changeInput(getControlByLabel('Name'), 'batch')
    changeInput(getControlByLabel('Quantity'), '2')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(2))

    expect(createdPayloads.length).toBe(2)
    expect(createdPayloads[0]?.name).toBe('batch')
    for (const payload of createdPayloads) {
      expect(payload.group).toBe('auto')
      expect(payload.auto_groups).toEqual([])
      expect(payload.cross_group_retry).toBe(true)
    }
  })

  test('preserves an unsaved custom order and mode after Auto to ordinary to Auto changes', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderCreateDrawer()

    const autoOrderControl = getControlByLabel('Selected groups')
    const addGroupTrigger = autoOrderControl.querySelector<HTMLButtonElement>(
      'button[role="combobox"]'
    )
    if (!addGroupTrigger) {
      throw new Error('Expected Selected groups combobox')
    }
    selectComboboxOption(addGroupTrigger, 'Priority access')

    expect(
      document.querySelector('button[aria-label="Remove vip"]')
    ).toBeTruthy()
    expect(document.body.textContent?.includes('1 / 3 groups selected')).toBe(
      true
    )
    expect(findButton('Use global groups', true).disabled).toBe(false)

    fireEvent.click(screen.getByRole('button', { name: 'Single group' }))
    const groupTrigger = getControlByLabel('Group')
    selectComboboxOption(groupTrigger, 'Standard access')
    expect(document.querySelector('button[aria-label="Remove vip"]')).toBe(null)
    fireEvent.click(screen.getByRole('button', { name: 'Multiple groups' }))

    expect(
      document.querySelector('button[aria-label="Remove vip"]')
    ).toBeTruthy()
    expect(document.body.textContent?.includes('1 / 3 groups selected')).toBe(
      true
    )
    expect(findButton('Use global groups', true).disabled).toBe(false)

    changeInput(getControlByLabel('Name'), 'custom')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(1))
    expect(createdPayloads[0]?.auto_groups).toEqual(['vip'])
  })

  test('switches a single group to multiple groups and submits both selected groups on one key', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderCreateDrawer(false)
    selectComboboxOption(getControlByLabel('Group'), 'Standard access')

    expect(
      screen.getByRole('button', { name: 'Single group' })
    ).toHaveAttribute('aria-pressed', 'true')
    fireEvent.click(screen.getByRole('button', { name: 'Multiple groups' }))
    expect(screen.getByRole('button', { name: 'Remove default' })).toBeVisible()
    const addGroupTrigger = within(
      getControlByLabel('Selected groups')
    ).getByRole<HTMLButtonElement>('combobox')
    selectComboboxOption(addGroupTrigger, 'Priority access')
    expect(screen.getByRole('button', { name: 'Remove vip' })).toBeVisible()

    changeInput(getControlByLabel('Name'), 'shared-models')
    fireEvent.click(findButton('Save changes', true))
    await waitFor(() => expect(createdPayloads).toHaveLength(1))
    expect(createdPayloads[0]).toMatchObject({
      group: 'auto',
      auto_groups: ['default', 'vip'],
      cross_group_retry: true,
    })
  })
})

describe('API keys mutate drawer user quota query switch', () => {
  test('sends false for a new key after the switch, which starts on, is turned off', async () => {
    const createdPayloads: Array<Record<string, unknown>> = []
    installApiFixtures(createdPayloads)
    await renderCreateDrawer()

    fireEvent.click(findButton('Advanced Settings', true))
    const quotaQuerySwitch = await screen.findByRole('switch', {
      name: 'Allow querying user quota',
    })
    expect(quotaQuerySwitch).toBeChecked()
    fireEvent.click(quotaQuerySwitch)
    expect(quotaQuerySwitch).not.toBeChecked()

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
    await renderCreateDrawer(true, models)

    expect(
      screen.queryByRole('radiogroup', { name: 'GPT speed tier' })
    ).not.toBeInTheDocument()
  })

  test('allows keyboard selection and sends the locked tier for every batch-created key', async () => {
    const user = userEvent.setup()
    const payloads: Array<Record<string, unknown>> = []
    const models = ['openai/gpt-6-astra', 'deepseek-chat']
    installApiFixtures(payloads, models)
    await renderCreateDrawer(true, models)

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
    await renderCreateDrawer(true, models)
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
    await renderCreateDrawer(false, models)
    selectComboboxOption(getControlByLabel('Group'), 'Priority access')
    expect(
      await screen.findByRole('radiogroup', { name: 'GPT speed tier' })
    ).toBeVisible()

    selectComboboxOption(getControlByLabel('Group'), 'Standard access')
    await waitFor(() =>
      expect(
        screen.queryByRole('radiogroup', { name: 'GPT speed tier' })
      ).not.toBeInTheDocument()
    )
  })
})
