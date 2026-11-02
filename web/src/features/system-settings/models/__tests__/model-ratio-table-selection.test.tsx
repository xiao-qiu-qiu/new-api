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
import { createRef, type ComponentProps } from 'react'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import {
  ModelRatioVisualEditor,
  type ModelRatioVisualEditorHandle,
} from '../model-ratio-visual-editor'

let client: QueryClient | undefined

afterEach(() => {
  client?.clear()
  localStorage.clear()
  window.getSelection()?.removeAllRanges()
  vi.restoreAllMocks()
})

async function renderEditor(
  overrides: Partial<ComponentProps<typeof ModelRatioVisualEditor>> = {}
) {
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/pricing') {
      return { data: { success: true, data: [], vendors: [] } }
    }
    return { data: { success: true, data: {} } }
  })
  const modelRatio = JSON.stringify({
    'gpt-4.1-mini': 0.2,
    'claude-sonnet': 1.5,
  })
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  client = queryClient
  const onChange = vi.fn()
  const props: ComponentProps<typeof ModelRatioVisualEditor> = {
    savedModelPrice: '{}',
    savedModelRatio: modelRatio,
    savedCacheRatio: '{}',
    savedCreateCacheRatio: '{}',
    savedCompletionRatio: '{}',
    savedImageRatio: '{}',
    savedAudioRatio: '{}',
    savedAudioCompletionRatio: '{}',
    savedBillingMode: '{}',
    savedBillingExpr: '{}',
    modelPrice: '{}',
    modelRatio,
    cacheRatio: '{}',
    createCacheRatio: '{}',
    completionRatio: '{}',
    imageRatio: '{}',
    audioRatio: '{}',
    audioCompletionRatio: '{}',
    billingMode: '{}',
    billingExpr: '{}',
    onChange,
    onSave: vi.fn(),
    isSaving: false,
    ...overrides,
  }
  const view = render(
    <QueryClientProvider client={client}>
      <ModelRatioVisualEditor {...props} />
    </QueryClientProvider>
  )
  // The real page has pricing metadata loaded long before the user drags over
  // a model name, so settle the pricing query before interacting.
  await waitFor(() => expect(queryClient.isFetching()).toBe(0))
  return {
    ...view,
    onChange,
    rerenderEditor: (
      next: Partial<ComponentProps<typeof ModelRatioVisualEditor>>
    ) =>
      view.rerender(
        <QueryClientProvider client={queryClient}>
          <ModelRatioVisualEditor {...props} {...next} />
        </QueryClientProvider>
      ),
  }
}

// A drag-select ends with mousedown/mouseup inside the row and the browser
// then fires `click` on the row. Only that final `click` is dispatched here so
// the text selection created by the drag is still active when the row opens
// the editor.
it('keeps the drag-selected model name highlighted when the row click opens the editor', async () => {
  await renderEditor()
  const row = await screen.findByRole('row', { name: /gpt-4\.1-mini/ })
  const nameCell = within(row).getByText('gpt-4.1-mini')
  window.getSelection()?.selectAllChildren(nameCell)
  expect(String(window.getSelection())).toBe('gpt-4.1-mini')

  fireEvent.click(nameCell)

  expect(
    await screen.findByRole('region', { name: 'Edit model pricing' })
  ).toBeInTheDocument()
  expect(nameCell).toBeInTheDocument()
  expect(String(window.getSelection())).toBe('gpt-4.1-mini')
})

it('keeps other rows mounted when a different model is opened for editing', async () => {
  await renderEditor()
  const targetRow = await screen.findByRole('row', { name: /gpt-4\.1-mini/ })
  const otherName = within(
    screen.getByRole('row', { name: /claude-sonnet/ })
  ).getByText('claude-sonnet')

  fireEvent.click(within(targetRow).getByText('gpt-4.1-mini'))

  expect(
    await screen.findByRole('region', { name: 'Edit model pricing' })
  ).toBeInTheDocument()
  expect(otherName).toBeInTheDocument()
})

it('opens the current group ratio policy after only that draft option changes', async () => {
  const { rerenderEditor } = await renderEditor({
    savedIgnoreGroupRatio: '{"gpt-4.1-mini":true}',
    ignoreGroupRatio: '{"gpt-4.1-mini":true}',
  })
  fireEvent.click(screen.getByText('gpt-4.1-mini'))
  expect(
    await screen.findByRole('switch', { name: 'Ignore group ratio' })
  ).toBeChecked()

  rerenderEditor({ ignoreGroupRatio: '{"gpt-4.1-mini":false}' })
  fireEvent.click(screen.getByText('gpt-4.1-mini'))

  await waitFor(() =>
    expect(
      screen.getByRole('switch', { name: 'Ignore group ratio' })
    ).not.toBeChecked()
  )
})

it('copies the source group ratio policy to selected models and keeps other policies', async () => {
  const ignoreGroupRatio = JSON.stringify({
    'gpt-4.1-mini': true,
    'claude-sonnet': false,
    'unrelated-model': true,
  })
  const { onChange } = await renderEditor({
    savedIgnoreGroupRatio: ignoreGroupRatio,
    ignoreGroupRatio,
  })
  fireEvent.click(
    within(screen.getByRole('row', { name: /claude-sonnet/ })).getByRole(
      'checkbox',
      { name: 'Select row' }
    )
  )
  fireEvent.click(screen.getByText('gpt-4.1-mini'))
  fireEvent.click(
    screen.getByRole('button', { name: 'Copy gpt-4.1-mini pricing' })
  )

  await waitFor(() =>
    expect(onChange).toHaveBeenCalledWith(
      'billing_setting.ignore_group_ratio',
      expect.any(String)
    )
  )
  const updated = onChange.mock.calls.find(
    ([key]) => key === 'billing_setting.ignore_group_ratio'
  )?.[1]
  expect(JSON.parse(updated)).toEqual({
    'unrelated-model': true,
    'claude-sonnet': true,
    'gpt-4.1-mini': true,
  })
})

it('saves an explicitly disabled group ratio policy without clearing other models', async () => {
  const editorRef = createRef<ModelRatioVisualEditorHandle>()
  const ignoreGroupRatio = JSON.stringify({
    'gpt-4.1-mini': true,
    'claude-sonnet': true,
  })
  const { onChange } = await renderEditor({
    ref: editorRef,
    savedIgnoreGroupRatio: ignoreGroupRatio,
    ignoreGroupRatio,
    onSave: async () => {
      await editorRef.current?.commitOpenEditor()
    },
  })
  fireEvent.click(screen.getByText('gpt-4.1-mini'))
  fireEvent.click(
    await screen.findByRole('switch', { name: 'Ignore group ratio' })
  )
  fireEvent.click(screen.getByRole('button', { name: 'Save model prices' }))

  await waitFor(() =>
    expect(onChange).toHaveBeenCalledWith(
      'billing_setting.ignore_group_ratio',
      expect.any(String)
    )
  )
  const updated = onChange.mock.calls.find(
    ([key]) => key === 'billing_setting.ignore_group_ratio'
  )?.[1]
  expect(JSON.parse(updated)).toEqual({
    'gpt-4.1-mini': false,
    'claude-sonnet': true,
  })
})

it('removes only the deleted model group ratio policy', async () => {
  const ignoreGroupRatio = JSON.stringify({
    'gpt-4.1-mini': true,
    'claude-sonnet': false,
  })
  const { onChange } = await renderEditor({
    savedIgnoreGroupRatio: ignoreGroupRatio,
    ignoreGroupRatio,
  })
  fireEvent.click(
    within(screen.getByRole('row', { name: /gpt-4\.1-mini/ })).getByRole(
      'button',
      { name: 'Open menu' }
    )
  )
  fireEvent.click(await screen.findByRole('menuitem', { name: 'Delete' }))

  expect(onChange).toHaveBeenCalledWith(
    'billing_setting.ignore_group_ratio',
    JSON.stringify({ 'claude-sonnet': false }, null, 2)
  )
})
