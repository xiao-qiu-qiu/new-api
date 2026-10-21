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
  act,
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useEffect } from 'react'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useChannelTestStore } from '@/stores/channel-test-store'

import { channelSchema, type ChannelTestResponse } from '../../types'
import { ChannelsProvider, useChannels } from '../channels-provider'
import { ChannelModelTestDialog } from '../dialogs/channel-model-test-dialog'
import { ChannelTestDialog } from '../dialogs/channel-test-dialog'

const channel = channelSchema.parse({
  id: 42,
  type: 1,
  key: '',
  name: 'Test channel',
  models: 'model-a,model-b',
  status: 1,
  created_time: 0,
  test_time: 0,
  response_time: 0,
  balance_updated_time: 0,
})
let client: QueryClient

function TestChannelFixture() {
  const { setCurrentRow } = useChannels()
  useEffect(() => setCurrentRow(channel), [setCurrentRow])
  return <ChannelTestDialog open onOpenChange={() => undefined} />
}

function renderChannelTest() {
  return render(
    <QueryClientProvider client={client}>
      <ChannelsProvider>
        <TestChannelFixture />
      </ChannelsProvider>
    </QueryClientProvider>
  )
}

beforeEach(() => {
  client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  localStorage.clear()
  useChannelTestStore.setState({ prompts: {} })
})

afterEach(() => {
  cleanup()
  client.clear()
  vi.restoreAllMocks()
  useChannelTestStore.setState({ prompts: {} })
  localStorage.clear()
})

it('runs the edited prompt from a keyboard-opened model dialog and displays the complete escaped reply', async () => {
  let resolveReply!: (value: { data: ChannelTestResponse }) => void
  const reply = new Promise<{ data: ChannelTestResponse }>((resolve) => {
    resolveReply = resolve
  })
  const post = vi.spyOn(api, 'post').mockReturnValue(reply)
  const user = userEvent.setup()
  const write = vi.spyOn(navigator.clipboard, 'writeText').mockResolvedValue()
  renderChannelTest()

  const entry = await screen.findByRole('button', {
    name: 'Prompt and response for model-a',
  })
  entry.focus()
  await user.keyboard('{Enter}')
  const details = within(
    await screen.findByRole('dialog', { name: 'Prompt and response' })
  )
  const prompt = details.getByRole('textbox', { name: 'Test prompt' })
  await user.type(prompt, 'Explain your answer step by step')
  await user.click(details.getByRole('button', { name: 'Test Connection' }))

  expect(prompt).toBeDisabled()
  expect(details.getByRole('button', { name: 'Testing...' })).toBeDisabled()
  expect(post).toHaveBeenCalledWith(
    '/api/channel/test/42',
    { model: 'model-a', prompt: 'Explain your answer step by step' },
    expect.any(Object)
  )

  const text = `Detailed reply\n${'A long explanation. '.repeat(45)}\n<img src=x onerror=alert(1)>`
  await act(async () => {
    resolveReply({
      data: {
        success: true,
        time: 0.5,
        response_text: text,
        response_model: 'upstream-model-a',
      },
    })
  })
  const output = details.getByRole('region', { name: 'Model response' })
  expect(output).toHaveTextContent('upstream-model-a')
  expect(output.querySelector('pre')).toHaveTextContent(
    text.replaceAll(/\s+/g, ' ')
  )
  expect(output.querySelector('img')).toBeNull()
  expect(prompt).toBeEnabled()
  await user.click(details.getByRole('button', { name: 'Copy response' }))
  expect(write).toHaveBeenCalledWith(text)

  await user.click(details.getByRole('button', { name: 'Use default prompt' }))
  expect(prompt).toHaveValue('')
  expect(details.getByText('Prompt used for this response')).toBeVisible()
  expect(details.getByText('Explain your answer step by step')).toBeVisible()
})

it('uses each model prompt during batch testing and opens the matching response afterwards', async () => {
  useChannelTestStore.getState().setPrompt(42, 'model-a', 'Question A')
  useChannelTestStore.getState().setPrompt(42, 'model-b', 'Question B')
  useChannelTestStore.getState().setPrompt(43, 'model-a', 'Another channel')
  const post = vi.spyOn(api, 'post').mockImplementation(async (_url, data) => {
    const request = data as { prompt: string }
    return {
      data: {
        success: true,
        time: 0.1,
        response_text: `Reply to ${request.prompt}`,
      },
    }
  })
  const user = userEvent.setup()
  renderChannelTest()

  await user.click(
    await screen.findByRole('button', { name: 'Test all 2 models' })
  )
  await waitFor(() =>
    expect(
      screen.getAllByRole('button', { name: 'View response' })
    ).toHaveLength(2)
  )
  expect(post.mock.calls.map(([, body]) => body)).toEqual([
    { model: 'model-a', prompt: 'Question A' },
    { model: 'model-b', prompt: 'Question B' },
  ])
  await user.click(screen.getAllByRole('button', { name: 'View response' })[1])
  const details = within(
    await screen.findByRole('dialog', { name: 'Prompt and response' })
  )
  expect(details.getByRole('textbox', { name: 'Test prompt' })).toHaveValue(
    'Question B'
  )
  expect(
    details.getByRole('region', { name: 'Model response' })
  ).toHaveTextContent('Reply to Question B')
})

it('shows empty results, partial failures and truncation without hiding received text', async () => {
  const props = {
    open: true,
    onOpenChange: vi.fn(),
    channelId: 42,
    channelName: 'Test channel',
    model: 'model-a',
    disabled: false,
    onTest: vi.fn(),
  }
  const view = render(<ChannelModelTestDialog {...props} />)
  expect(screen.getByText('No response yet')).toBeVisible()
  view.rerender(
    <ChannelModelTestDialog {...props} result={{ status: 'success' }} />
  )
  expect(screen.getByText('No text returned')).toBeVisible()
  view.rerender(
    <ChannelModelTestDialog
      {...props}
      result={{
        status: 'error',
        error: 'Stream interrupted',
        responseText: 'Partial reply',
        responseTruncated: true,
      }}
    />
  )
  expect(screen.getByRole('alert')).toHaveTextContent('Stream interrupted')
  expect(screen.getByText('Partial reply')).toBeVisible()
  expect(screen.getByText('Response truncated')).toBeVisible()
})
