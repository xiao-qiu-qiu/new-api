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
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'
import { useAuthStore } from '@/stores/auth-store'

import { ModelMutateDrawer } from '../components/drawers/model-mutate-drawer'
import { ModelVideoCapabilities } from '../components/model-video-capabilities'
import {
  videoCapabilitiesDraft,
  videoCapabilitiesPayload,
  videoCapabilitiesSchema,
  type VideoCapabilities,
} from '../lib/video-capabilities'

afterEach(() => {
  cleanup()
  useAuthStore.getState().auth.reset()
})

it('preserves an explicit zero reference limit and rejects durations above the maximum', () => {
  const draft = videoCapabilitiesDraft({
    max_reference_audio: 0,
    max_duration_seconds: 10,
    durations: [5, 10],
  })
  expect(
    videoCapabilitiesPayload(videoCapabilitiesSchema.parse(draft))
  ).toEqual({
    max_reference_audio: 0,
    max_duration_seconds: 10,
    durations: [5, 10],
  })
  expect(
    videoCapabilitiesSchema.safeParse({ ...draft, durations: ['5', '15'] })
      .success
  ).toBe(false)
  expect(
    videoCapabilitiesSchema.safeParse({ ...draft, max_reference_audio: '-1' })
      .success
  ).toBe(false)
  expect(videoCapabilitiesPayload(videoCapabilitiesDraft(null))).toBeNull()
})

it('preserves drafts after reload failure, saves only the selected model and resets overrides after confirmation', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  const snapshot = {
    model_name: 'video-a',
    video_capabilities: { max_reference_audio: 2, max_duration_seconds: 15 },
    version: 'v1',
  }
  const get = vi.spyOn(api, 'get').mockResolvedValue({
    data: { success: true, data: snapshot },
  })
  const patch = vi
    .spyOn(api, 'patch')
    .mockImplementation(async (_url, body) => ({
      data: {
        success: true,
        data: {
          ...snapshot,
          video_capabilities: (
            body as { video_capabilities: VideoCapabilities | null }
          ).video_capabilities,
          version: 'v2',
        },
      },
    }))
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  render(
    <QueryClientProvider client={client}>
      <ModelVideoCapabilities modelName='video-a' onDirtyChange={() => {}} />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  const audio = await screen.findByRole('spinbutton', {
    name: 'Maximum reference audio files',
  })
  expect(audio).toHaveValue(2)
  expect(
    screen.getByRole('combobox', { name: 'Allowed resolutions' })
  ).toHaveAttribute('aria-invalid', 'false')
  expect(
    screen.getByRole('combobox', { name: 'Allowed aspect ratios' })
  ).toBeVisible()
  expect(
    screen.getByRole('combobox', { name: 'Allowed durations (seconds)' })
  ).toBeVisible()
  await user.clear(audio)
  await user.type(audio, '0')
  get.mockRejectedValueOnce(new Error('Connection dropped'))
  await user.click(
    screen.getByRole('button', { name: 'Reload video capabilities' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Reload video capabilities',
    })
  )
  expect(await screen.findByText('Connection dropped')).toBeVisible()
  expect(
    screen.getByRole('spinbutton', { name: 'Maximum reference audio files' })
  ).toHaveValue(0)
  await user.click(
    screen.getByRole('button', { name: 'Save video capabilities' })
  )
  await waitFor(() =>
    expect(patch).toHaveBeenCalledWith('/api/option/video_model_capabilities', {
      model_name: 'video-a',
      video_capabilities: { max_reference_audio: 0, max_duration_seconds: 15 },
      expected_version: 'v1',
    })
  )
  await waitFor(() =>
    expect(
      screen.getByRole('button', { name: 'Save video capabilities' })
    ).toBeDisabled()
  )
  await user.click(
    screen.getByRole('button', { name: 'Restore model defaults' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Restore model defaults',
    })
  )
  await waitFor(() =>
    expect(patch).toHaveBeenLastCalledWith(
      '/api/option/video_model_capabilities',
      {
        model_name: 'video-a',
        video_capabilities: null,
        expected_version: 'v2',
      }
    )
  )
  client.clear()
})

it('keeps video drafts when switching tabs and confirms before closing with unsaved changes', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'root', role: 100 })
  vi.spyOn(api, 'get').mockImplementation(async (url) => {
    if (url === '/api/option/video_model_capabilities') {
      return {
        data: {
          success: true,
          data: {
            model_name: 'video-a',
            video_capabilities: null,
            version: 'v1',
          },
        },
      }
    }
    return { data: { success: true, data: { items: [] } } }
  })
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  const close = vi.fn()
  render(
    <QueryClientProvider client={client}>
      <ModelMutateDrawer
        open
        onOpenChange={close}
        currentRow={{
          id: 0,
          model_name: 'video-a',
          status: 1,
          sync_official: 1,
          name_rule: 0,
          created_time: 1,
          updated_time: 1,
        }}
      />
    </QueryClientProvider>
  )
  const user = userEvent.setup()
  await user.click(screen.getByRole('tab', { name: 'Video capabilities' }))
  const duration = await screen.findByRole('spinbutton', {
    name: 'Maximum duration (seconds)',
  })
  await user.type(duration, '12')
  await user.click(screen.getByRole('tab', { name: 'Model metadata' }))
  await user.click(screen.getByRole('tab', { name: 'Video capabilities' }))
  expect(
    screen.getByRole('spinbutton', { name: 'Maximum duration (seconds)' })
  ).toHaveValue(12)
  await user.click(screen.getByRole('button', { name: 'Close' }))
  expect(await screen.findByRole('alertdialog')).toHaveTextContent(
    'Discard unsaved changes?'
  )
  expect(close).not.toHaveBeenCalled()
  client.clear()
})
