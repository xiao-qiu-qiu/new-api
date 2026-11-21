import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import { SettingsPageProvider } from '../../components/settings-page-context'
import { DistillationGuardSection } from '../distillation-guard-section'

test('validates a streak and saves the complete policy in one update', async () => {
  const initial = {
    enabled: true,
    min_output_tokens: 3000,
    max_cached_tokens: 0,
    consecutive_requests: 10,
    window_minutes: 5,
    block_minutes: 10,
  }
  const put = vi
    .spyOn(api, 'put')
    .mockResolvedValue({ data: { success: true } })
  const actions = document.body.appendChild(document.createElement('div'))
  render(
    <QueryClientProvider client={new QueryClient()}>
      <SettingsPageProvider actionsContainer={actions}>
        <DistillationGuardSection policy={JSON.stringify(initial)} />
      </SettingsPageProvider>
    </QueryClientProvider>
  )
  const count = screen.getByRole('spinbutton', {
    name: 'Consecutive matching requests',
  })
  const save = screen.getByRole('button', { name: 'Save Changes' })
  expect(count).toHaveValue(10)
  fireEvent.change(count, { target: { value: '0' } })
  await waitFor(() => expect(count).toHaveAttribute('aria-invalid', 'true'))
  expect(save).toBeDisabled()
  fireEvent.change(count, { target: { value: '8' } })
  fireEvent.click(
    screen.getByRole('switch', { name: 'Limit non-streaming text generation' })
  )
  await waitFor(() => expect(save).toBeEnabled())
  fireEvent.click(save)
  await waitFor(() => expect(put).toHaveBeenCalledTimes(1))
  const payload = put.mock.calls[0][1] as { key: string; value: string }
  expect(payload.key).toBe('DistillationGuardPolicy')
  expect(JSON.parse(payload.value)).toEqual({
    ...initial,
    enabled: false,
    consecutive_requests: 8,
  })
  actions.remove()
})
