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
import { act, renderHook, waitFor } from '@testing-library/react'
import { toast, type Action } from 'sonner'
import { afterEach, assert, beforeEach, expect, test, vi } from 'vitest'

import { useAuthStore } from '@/stores/auth-store'

import { STORAGE_KEYS } from '../../../constants'
import { usePlaygroundState } from '../../../hooks/use-playground-state'
import type { Message } from '../../../types'
import {
  clearPlaygroundData,
  loadConfig,
  loadMessages,
  loadParameterEnabled,
  saveConfig,
  saveMessages,
  saveParameterEnabled,
} from '../storage'

const messages: Message[] = [
  {
    key: 'a-chat',
    from: 'user',
    versions: [{ id: 'a-version', content: 'Account A private chat' }],
  },
]

let records: Map<string, unknown>
let failWrites: boolean
let failReads: boolean

function createMessageDatabaseFixture() {
  return {
    open: () => {
      const request = {
        result: {
          close: () => undefined,
          transaction: (_store: string, mode: string) => {
            let commit = () => undefined
            const transaction = {
              error: null as DOMException | null,
              oncomplete: null as (() => void) | null,
              onabort: null as (() => void) | null,
              objectStore: () => ({
                get: (key: string) => ({
                  result: structuredClone(records.get(key)),
                }),
                put: (value: unknown, key: string) => {
                  commit = () => {
                    records.set(key, structuredClone(value))
                  }
                  return { result: key }
                },
                delete: (key: string) => {
                  commit = () => {
                    records.delete(key)
                  }
                  return { result: undefined }
                },
              }),
            }
            queueMicrotask(() => {
              if (
                (mode === 'readwrite' && failWrites) ||
                (mode === 'readonly' && failReads)
              ) {
                transaction.error = new DOMException(
                  'Disk full',
                  'QuotaExceededError'
                )
                transaction.onabort?.()
                return
              }
              commit()
              transaction.oncomplete?.()
            })
            return transaction
          },
        },
        onsuccess: null as (() => void) | null,
      }
      queueMicrotask(() => request.onsuccess?.())
      return request
    },
  }
}

beforeEach(() => {
  localStorage.clear()
  useAuthStore.getState().auth.reset()
  records = new Map()
  failWrites = false
  failReads = false
  vi.stubGlobal('indexedDB', createMessageDatabaseFixture())
})

afterEach(() => {
  vi.useRealTimers()
  localStorage.clear()
  useAuthStore.getState().auth.reset()
  vi.unstubAllGlobals()
})

test('switching accounts isolates config, messages and parameter toggles and preserves returning account data', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
  saveConfig({ model: 'account-a-model' })
  await saveMessages(messages)
  saveParameterEnabled({ temperature: true })

  useAuthStore.getState().auth.reset()
  useAuthStore.getState().auth.setUser({ id: 2, username: 'B', role: 1 })
  expect(loadConfig()).toEqual({})
  expect(await loadMessages()).toBeNull()
  expect(loadParameterEnabled()).toEqual({})
  saveConfig({ model: 'account-b-model' })
  await saveMessages([])
  saveParameterEnabled({ temperature: false })

  useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
  expect(loadConfig()).toEqual({ model: 'account-a-model' })
  expect(await loadMessages()).toEqual(messages)
  expect(loadParameterEnabled()).toEqual({ temperature: true })

  await clearPlaygroundData()
  expect(await loadMessages()).toBeNull()
  useAuthStore.getState().auth.setUser({ id: 2, username: 'B', role: 1 })
  expect(loadConfig()).toEqual({ model: 'account-b-model' })
  expect(await loadMessages()).toEqual([])
  expect(loadParameterEnabled()).toEqual({ temperature: false })
})

test('legacy global data is removed instead of assigned to the first signed-in account', async () => {
  localStorage.setItem(
    STORAGE_KEYS.CONFIG,
    JSON.stringify({ model: 'legacy-model' })
  )
  localStorage.setItem(STORAGE_KEYS.MESSAGES, JSON.stringify(messages))
  localStorage.setItem(
    STORAGE_KEYS.PARAMETER_ENABLED,
    JSON.stringify({ temperature: true })
  )
  useAuthStore.getState().auth.setUser({ id: 2, username: 'B', role: 1 })

  expect(loadConfig()).toEqual({})
  expect(await loadMessages()).toBeNull()
  expect(loadParameterEnabled()).toEqual({})
  for (const key of Object.values(STORAGE_KEYS)) {
    expect(localStorage.getItem(key)).toBeNull()
  }
})

test('storage calls without a signed-in user do not persist shared data', async () => {
  saveConfig({ model: 'signed-out-model' })
  await saveMessages(messages)
  saveParameterEnabled({ temperature: true })
  expect(loadConfig()).toEqual({})
  expect(await loadMessages()).toBeNull()
  expect(loadParameterEnabled()).toEqual({})
  expect(localStorage.length).toBe(0)
  expect(records.size).toBe(0)

  useAuthStore.getState().auth.setUser({ id: 2, username: 'B', role: 1 })
  await saveMessages(messages, null)
  expect(await loadMessages(null)).toBeNull()
  expect(records.size).toBe(0)
})

test('image conversations larger than the legacy localStorage limit survive a storage round trip', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
  const imageMessages: Message[] = [
    {
      ...messages[0],
      attachments: [
        {
          type: 'file',
          filename: 'photo.png',
          mediaType: 'image/png',
          url: `data:image/png;base64,${'A'.repeat(1024 * 1024 + 4)}`,
        },
      ],
    },
  ]

  await saveMessages(imageMessages)

  expect(localStorage.getItem(`${STORAGE_KEYS.MESSAGES}:1`)).toBeNull()
  expect(await loadMessages()).toEqual(imageMessages)
})

test('legacy account text messages migrate only after IndexedDB commits successfully', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
  const key = `${STORAGE_KEYS.MESSAGES}:1`
  const legacy = JSON.stringify({ version: 1, data: messages })
  localStorage.setItem(key, legacy)
  failWrites = true
  vi.spyOn(console, 'error').mockImplementation(() => undefined)

  expect(await loadMessages()).toEqual(messages)
  expect(localStorage.getItem(key)).toBe(legacy)
  expect(records.size).toBe(0)

  failWrites = false
  expect(await loadMessages()).toEqual(messages)
  expect(localStorage.getItem(key)).toBeNull()
  expect(records.get(key)).toEqual({ version: 1, data: messages })
})

test('an in-flight save keeps its original account owner after the active account changes', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
  const saving = saveMessages(messages)
  useAuthStore.getState().auth.setUser({ id: 2, username: 'B', role: 1 })
  await saving

  expect(await loadMessages()).toBeNull()
  useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
  expect(await loadMessages()).toEqual(messages)
})

test.each(['timer', 'unmount'] as const)(
  'a pending %s save stays with the hook owner after switching accounts',
  async (flush) => {
    useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
    const { result, unmount } = renderHook(() => usePlaygroundState())
    await waitFor(() => expect(result.current.isLoadingMessages).toBe(false))
    vi.useFakeTimers()
    act(() => result.current.updateMessages(messages))
    useAuthStore.getState().auth.setUser({ id: 2, username: 'B', role: 1 })

    await act(async () => {
      if (flush === 'timer') {
        await vi.advanceTimersByTimeAsync(500)
      } else {
        unmount()
      }
    })

    expect(await loadMessages(1)).toEqual(messages)
    expect(await loadMessages(2)).toBeNull()
    unmount()
  }
)

test('a failed history read rejects and keeps hook updates from overwriting saved history', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
  await saveMessages(messages)
  failReads = true
  await expect(loadMessages()).rejects.toMatchObject({
    name: 'QuotaExceededError',
  })
  const { result, unmount } = renderHook(() => usePlaygroundState())
  await waitFor(() => expect(result.current.isLoadingMessages).toBe(false))
  expect(result.current.isMessageStorageReady).toBe(false)

  act(() => result.current.updateMessages([]))
  unmount()
  failReads = false

  expect(await loadMessages()).toEqual(messages)
})

test('large image writes reject on storage failure and the hook reports the unsaved history', async () => {
  useAuthStore.getState().auth.setUser({ id: 1, username: 'A', role: 1 })
  await saveMessages(messages)
  const { result, unmount } = renderHook(() => usePlaygroundState())
  await waitFor(() => expect(result.current.isMessageStorageReady).toBe(true))
  const imageMessages: Message[] = [
    {
      ...messages[0],
      attachments: [
        {
          type: 'file',
          mediaType: 'image/png',
          url: `data:image/png;base64,${'A'.repeat(1024 * 1024 + 4)}`,
        },
      ],
    },
  ]
  failWrites = true
  const notify = vi.spyOn(toast, 'error').mockReturnValue('storage-error')

  await expect(saveMessages(imageMessages)).rejects.toMatchObject({
    name: 'QuotaExceededError',
  })
  vi.useFakeTimers()
  act(() => result.current.updateMessages(imageMessages))
  await act(async () => {
    await vi.advanceTimersByTimeAsync(500)
  })

  expect(notify).toHaveBeenCalledWith(
    'Failed to save conversation history. Keep this page open and try again.',
    {
      id: 'playground-message-save-error',
      action: { label: 'Retry', onClick: expect.any(Function) },
    }
  )
  expect(records.get(`${STORAGE_KEYS.MESSAGES}:1`)).toEqual({
    version: 1,
    data: messages,
  })
  failWrites = false
  const retry = notify.mock.calls[0][1]?.action as Action | undefined
  assert(retry)
  await act(async () => {
    retry.onClick({} as Parameters<Action['onClick']>[0])
  })
  expect(records.get(`${STORAGE_KEYS.MESSAGES}:1`)).toEqual({
    version: 1,
    data: imageMessages,
  })
  unmount()
})
