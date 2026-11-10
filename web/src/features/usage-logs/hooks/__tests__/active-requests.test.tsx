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
  focusManager,
  QueryClient,
  QueryClientProvider,
  useQuery,
} from '@tanstack/react-query'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import type { AxiosAdapter } from 'axios'
import type { PropsWithChildren } from 'react'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { api } from '@/lib/api'

import { usageLogSchema } from '../../data/schema'
import {
  getActiveRequest,
  getActiveRequests,
  mergeActiveLogs,
  type ActiveRequest,
  type ActiveRequestSnapshot,
} from '../../lib/active-requests'
import { fetchLogsByCategory } from '../../lib/utils'
import {
  useActiveRequests,
  type UseActiveRequestsOptions,
} from '../use-active-requests'

const activeRequest: ActiveRequest = {
  request_id: 'request-a',
  user_id: 7,
  username: 'viewer',
  token_id: 3,
  token_name: 'test key',
  model_name: 'test-model',
  group: 'default',
  channel: 4,
  channel_name: 'upstream',
  started_at: 1700000000000,
  updated_at: 1700000005000,
  status: 'streaming',
  is_stream: true,
  attempt: 1,
  client_gone: false,
}
const snapshot: ActiveRequestSnapshot = {
  items: [activeRequest],
  total: 1,
  server_time: 1700000005000,
  received_at: 1700000003000,
}
const options: UseActiveRequestsOptions = {
  enabled: true,
  isAdmin: false,
  viewAccess: 'self',
  userId: 7,
  searchParams: {},
  columnFilters: [],
}
const persistedLog = usageLogSchema.parse({
  id: 8,
  user_id: 7,
  created_at: 1700000005,
  type: 2,
  content: '',
  request_id: 'request-a',
})

function activeResponse(items = [activeRequest]) {
  return {
    data: {
      success: true,
      message: '',
      data: {
        items,
        total: items.length,
        server_time: snapshot.server_time,
      },
    },
  }
}

let client: QueryClient

function wrapper(props: PropsWithChildren) {
  return (
    <QueryClientProvider client={client}>{props.children}</QueryClientProvider>
  )
}

beforeEach(() => {
  client = new QueryClient({
    defaultOptions: { queries: { retry: false, gcTime: Infinity } },
  })
  focusManager.setFocused(true)
})

afterEach(() => {
  cleanup()
  client.clear()
  focusManager.setFocused(undefined)
  vi.useRealTimers()
})

describe('active request data', () => {
  it.each([
    [false, '/api/log/self/active'],
    [true, '/api/log/active'],
  ] as const)(
    'loads the %s view with encoded filters and its own abort signal',
    async (isAdmin, endpoint) => {
      const get = vi.spyOn(api, 'get').mockResolvedValue(activeResponse())
      const signal = new AbortController().signal
      const result = await getActiveRequests(
        { model_name: 'test/model + variant', type: 0 },
        isAdmin,
        signal
      )

      const [url, config] = get.mock.calls[0]
      const parsed = new URL(url, 'http://localhost')
      expect(parsed.pathname).toBe(endpoint)
      expect(parsed.searchParams.get('model_name')).toBe('test/model + variant')
      expect(parsed.searchParams.get('type')).toBe('0')
      expect(config).toMatchObject({ signal, disableDuplicate: true })
      expect(result).toMatchObject({
        items: [activeRequest],
        total: 1,
        server_time: snapshot.server_time,
        received_at: expect.any(Number),
      })
    }
  )

  it('rejects a business failure and a missing payload instead of returning an empty snapshot', async () => {
    vi.spyOn(api, 'get')
      .mockResolvedValueOnce({
        data: { success: false, message: 'Unavailable' },
      })
      .mockResolvedValueOnce({ data: { success: true } })

    await expect(getActiveRequests({}, false)).rejects.toMatchObject({
      message: 'Unavailable',
      cause: { success: false, message: 'Unavailable' },
    })
    await expect(getActiveRequests({}, false)).rejects.toThrow()
  })

  it('adds active rows with a server clock offset and preserves intermediate errors', () => {
    const errorLog = { ...persistedLog, id: 6, type: 5 }
    const unrelatedLog = { ...persistedLog, id: 7, request_id: 'other' }
    const logs = [persistedLog, errorLog, unrelatedLog]

    const merged = mergeActiveLogs(logs, snapshot)

    expect(merged).toHaveLength(3)
    expect(merged[0]).toMatchObject({
      id: -1,
      request_id: 'request-a',
      created_at: 1700000000,
      type: 2,
      model_name: 'test-model',
      channel: 4,
      quota: 0,
      prompt_tokens: 0,
      completion_tokens: 0,
    })
    expect(getActiveRequest(merged[0])).toEqual({
      ...activeRequest,
      clock_offset_ms: 2000,
    })
    expect(merged.slice(1)).toEqual([errorLog, unrelatedLog])
    expect(logs).toEqual([persistedLog, errorLog, unrelatedLog])
    expect(getActiveRequest(persistedLog)).toBeUndefined()
  })

  it('restores persisted consumption when the active request disappears', () => {
    const logs = [persistedLog]

    expect(mergeActiveLogs(logs, { ...snapshot, items: [], total: 0 })).toBe(
      logs
    )
    expect(mergeActiveLogs(logs)).toBe(logs)
    expect(mergeActiveLogs([], snapshot)[0].request_id).toBe('request-a')
  })
})

describe('active request polling', () => {
  it.each([
    { errorKeyword: 'blocked by policy' },
    { errorCode: 'cyber_policy' },
    { requestResult: 'failed' },
    { requestResult: 'success' },
    { requestResult: 'stream_error' },
  ])(
    'hides pending requests when filtering completed outcomes with %j',
    async (searchParams) => {
      const get = vi.spyOn(api, 'get').mockResolvedValue(activeResponse())
      const hook = renderHook(
        (filters: Record<string, unknown>) =>
          useActiveRequests({ ...options, searchParams: filters }),
        { wrapper, initialProps: {} as Record<string, unknown> }
      )
      await waitFor(() =>
        expect(hook.result.current.data?.items).toHaveLength(1)
      )
      get.mockClear()
      hook.rerender(searchParams)
      expect(hook.result.current.data).toBeUndefined()
      expect(hook.result.current.fetchStatus).toBe('idle')
      expect(get).not.toHaveBeenCalled()
    }
  )

  it('applies the same search and column filter precedence as persisted logs', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(activeResponse())
    const hook = renderHook(
      () =>
        useActiveRequests({
          ...options,
          isAdmin: true,
          viewAccess: 'admin',
          searchParams: {
            pageSize: 50,
            type: ['0'],
            model: 'search-model',
            token: 'search-token',
            username: 'viewer',
            channel: '4',
            requestId: 'request-a',
            upstreamRequestId: 'upstream-a',
            startTime: 1700000000000,
            endTime: 1700000010000,
          },
          columnFilters: [
            { id: 'type', value: ['2'] },
            { id: 'model_name', value: 'column-model' },
            { id: 'token_name', value: 'column-token' },
            { id: 'group', value: 'premium' },
          ],
        }),
      { wrapper }
    )

    await waitFor(() => expect(hook.result.current.data?.total).toBe(1))

    const url = new URL(get.mock.calls[0][0], 'http://localhost')
    expect(Object.fromEntries(url.searchParams)).toEqual({
      p: '1',
      page_size: '50',
      type: '2',
      model_name: 'column-model',
      token_name: 'column-token',
      username: 'viewer',
      channel: '4',
      group: 'premium',
      request_id: 'request-a',
      upstream_request_id: 'upstream-a',
      start_timestamp: '1700000000',
      end_timestamp: '1700000010',
    })
  })

  it('polls every two seconds, keeps an implicit time range stable across renders, and pauses while hidden', async () => {
    vi.useFakeTimers()
    const get = vi.spyOn(api, 'get').mockResolvedValue(activeResponse())
    const hook = renderHook((props) => useActiveRequests(props), {
      wrapper,
      initialProps: options,
    })
    await act(async () => vi.advanceTimersByTimeAsync(0))
    expect(hook.result.current.data?.items).toEqual([activeRequest])
    expect(get).toHaveBeenCalledTimes(1)

    await act(async () => vi.advanceTimersByTimeAsync(1000))
    hook.rerender({ ...options, searchParams: {}, columnFilters: [] })
    expect(hook.result.current.data?.items).toEqual([activeRequest])
    expect(get).toHaveBeenCalledTimes(1)
    await act(async () => vi.advanceTimersByTimeAsync(1000))
    expect(get).toHaveBeenCalledTimes(2)

    act(() => focusManager.setFocused(false))
    expect(hook.result.current.data).toBeUndefined()
    await act(async () => vi.advanceTimersByTimeAsync(6000))
    expect(get).toHaveBeenCalledTimes(2)

    await act(async () => {
      focusManager.setFocused(true)
      await vi.advanceTimersByTimeAsync(0)
    })
    expect(get).toHaveBeenCalledTimes(3)
    expect(hook.result.current.data?.items).toEqual([activeRequest])
  })

  it.each([
    { ...options, enabled: false },
    { ...options, userId: undefined },
    { ...options, searchParams: { type: ['5'] } },
    {
      ...options,
      searchParams: { type: ['2'] },
      columnFilters: [{ id: 'type', value: ['5'] }],
    },
  ])(
    'does not fetch when disabled or the selected type excludes consumption: %j',
    (props) => {
      const get = vi.spyOn(api, 'get').mockResolvedValue(activeResponse())
      const hook = renderHook(() => useActiveRequests(props), { wrapper })

      expect(get).not.toHaveBeenCalled()
      expect(hook.result.current.data).toBeUndefined()
    }
  )

  it('aborts only the hidden active poll, retains its cache and ignores its late completion', async () => {
    let resolvePoll!: (value: ReturnType<typeof activeResponse>) => void
    const get = vi
      .spyOn(api, 'get')
      .mockResolvedValueOnce(activeResponse())
      .mockReturnValueOnce(
        new Promise((resolve) => {
          resolvePoll = resolve
        })
      )
      .mockResolvedValue(activeResponse([]))
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const hook = renderHook(() => useActiveRequests(options), { wrapper })
    await waitFor(() => expect(hook.result.current.data?.total).toBe(1))
    const activeKey = client.getQueryCache().getAll()[0].queryKey
    const cached = client.getQueryData(activeKey)
    let refresh!: ReturnType<typeof hook.result.current.refetch>
    act(() => {
      refresh = hook.result.current.refetch()
    })
    const signal = get.mock.calls[1][1]?.signal
    const unrelatedKey = ['active-requests', 8, 'self']
    const unrelatedController = new AbortController()
    client.setQueryData(unrelatedKey, snapshot)
    let resolveOther!: () => void
    const unrelated = client
      .fetchQuery({
        queryKey: unrelatedKey,
        queryFn: ({ signal: otherSignal }) => {
          otherSignal.addEventListener('abort', () =>
            unrelatedController.abort()
          )
          return new Promise((resolve) => {
            resolveOther = () => resolve(snapshot)
          })
        },
      })
      .catch(() => undefined)

    act(() => focusManager.setFocused(false))

    expect(signal?.aborted).toBe(true)
    expect(unrelatedController.signal.aborted).toBe(false)
    expect(client.getQueryData(activeKey)).toBe(cached)
    await act(async () => {
      resolvePoll(activeResponse([]))
      await refresh
    })
    expect(invalidate).not.toHaveBeenCalled()
    expect(client.getQueryData(activeKey)).toBe(cached)

    await act(async () => {
      focusManager.setFocused(true)
      resolveOther()
      await unrelated
    })
    await waitFor(() => expect(hook.result.current.data?.items).toEqual([]))
    expect(get).toHaveBeenCalledTimes(3)
    expect(invalidate).toHaveBeenCalledOnce()
  })

  it('rechecks visibility before handing off a completed poll even before effects run', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValueOnce(activeResponse())
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const hook = renderHook(() => useActiveRequests(options), { wrapper })
    await waitFor(() => expect(hook.result.current.data?.total).toBe(1))
    get.mockImplementationOnce(async () => {
      vi.spyOn(focusManager, 'isFocused').mockReturnValue(false)
      return activeResponse([])
    })

    await act(async () => {
      await hook.result.current.refetch()
    })

    expect(invalidate).not.toHaveBeenCalled()
    const activeQuery = client
      .getQueryCache()
      .find({ queryKey: ['active-requests'], exact: false })
    expect(activeQuery?.state.data).toMatchObject({ items: [activeRequest] })
  })

  it.each([undefined, [], ['0'], ['2']])(
    'enables polling for the selected consumption type %j',
    async (type) => {
      vi.spyOn(api, 'get').mockResolvedValue(activeResponse())
      const hook = renderHook(
        () => useActiveRequests({ ...options, searchParams: { type } }),
        { wrapper }
      )

      await waitFor(() => expect(hook.result.current.data?.total).toBe(1))
    }
  )

  it('hides cached rows immediately when the page becomes disabled', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(activeResponse())
    const hook = renderHook((props) => useActiveRequests(props), {
      wrapper,
      initialProps: options,
    })
    await waitFor(() => expect(hook.result.current.data?.total).toBe(1))

    hook.rerender({ ...options, enabled: false })

    expect(hook.result.current.data).toBeUndefined()
    expect(get).toHaveBeenCalledTimes(1)
  })

  it('hides cached rows for an excluded type even when that query has prefetched data', async () => {
    const get = vi.spyOn(api, 'get').mockResolvedValue(activeResponse())
    const hook = renderHook(
      () => useActiveRequests({ ...options, searchParams: { type: ['5'] } }),
      { wrapper }
    )
    const query = client.getQueryCache().getAll()[0]

    await act(async () => {
      client.setQueryData(query.queryKey, snapshot)
    })

    expect(hook.result.current.data).toBeUndefined()
    expect(get).not.toHaveBeenCalled()
  })

  it.each([
    { ...options, userId: 8 },
    { ...options, isAdmin: true, viewAccess: 'admin' },
    { ...options, viewAccess: 'other-permission' },
    { ...options, searchParams: { model: 'other-model' } },
    { ...options, columnFilters: [{ id: 'group', value: 'premium' }] },
    { ...options, searchParams: { startTime: 1700000000000 } },
  ])(
    'does not show another account, permission, or filter cache while loading: %j',
    async (nextOptions) => {
      const get = vi.spyOn(api, 'get').mockResolvedValueOnce(activeResponse())
      const hook = renderHook((props) => useActiveRequests(props), {
        wrapper,
        initialProps: options,
      })
      await waitFor(() => expect(hook.result.current.data?.total).toBe(1))
      let resolve!: (value: ReturnType<typeof activeResponse>) => void
      get.mockReturnValueOnce(
        new Promise((done) => {
          resolve = done
        })
      )

      hook.rerender(nextOptions)

      expect(hook.result.current.data).toBeUndefined()
      await act(async () => resolve(activeResponse([])))
      await waitFor(() => expect(hook.result.current.data?.items).toEqual([]))
    }
  )

  it('ignores a late response from the previous account after aborting it', async () => {
    let resolveOld!: (value: ReturnType<typeof activeResponse>) => void
    const get = vi
      .spyOn(api, 'get')
      .mockReturnValueOnce(
        new Promise((done) => {
          resolveOld = done
        })
      )
      .mockResolvedValueOnce(activeResponse([]))
    const hook = renderHook((props) => useActiveRequests(props), {
      wrapper,
      initialProps: options,
    })
    const signal = get.mock.calls[0][1]?.signal

    hook.rerender({ ...options, userId: 8 })
    await waitFor(() => expect(hook.result.current.data?.items).toEqual([]))
    expect(signal?.aborted).toBe(true)
    await act(async () => resolveOld(activeResponse()))

    expect(hook.result.current.data?.items).toEqual([])
  })

  it('reports a failed poll as an error with automatic error toasts disabled', async () => {
    vi.spyOn(api, 'get').mockResolvedValue({
      data: { success: false, message: 'Unavailable' },
    })
    const hook = renderHook(() => useActiveRequests(options), { wrapper })

    await waitFor(() => expect(hook.result.current.isError).toBe(true))
    expect(hook.result.current.error?.message).toBe('Unavailable')
    expect(hook.result.current.data).toBeUndefined()
    expect(client.getQueryCache().getAll()[0].meta?.errorToast).toBe(false)
  })

  it.each([false, true])(
    'starts a fresh log HTTP read during handoff while an older response is pending (admin: %s)',
    async (isAdmin) => {
      const originalAdapter = api.defaults.adapter
      const scopedOptions = {
        ...options,
        isAdmin,
        viewAccess: isAdmin ? 'admin' : 'self',
        searchParams: {
          startTime: 1700000000000,
          endTime: 1700000010000,
        },
      }
      const logKey = ['logs', 'common', scopedOptions.viewAccess, 1, 20, {}, 7]
      const logPath = isAdmin ? '/api/log' : '/api/log/self'
      const activePath = `${logPath}/active`
      const reads: Array<{
        url: string
        signal?: { readonly aborted: boolean }
        resolve: (items: (typeof persistedLog)[]) => void
      }> = []
      let activeItems = [activeRequest]
      const adapter: AxiosAdapter = (config) => {
        const url = new URL(config.url ?? '', 'http://localhost')
        const response = {
          config,
          status: 200,
          statusText: 'OK',
          headers: {},
        }
        if (url.pathname === activePath) {
          return Promise.resolve({
            ...response,
            data: activeResponse(activeItems).data,
          })
        }
        if (url.pathname !== logPath) {
          throw new Error(`Unexpected GET ${config.url}`)
        }
        return new Promise((resolve) => {
          const resolveRead = (items: (typeof persistedLog)[]) => {
            resolve({
              ...response,
              data: {
                success: true,
                data: { items, total: items.length, page: 1, page_size: 20 },
              },
            })
          }
          reads.push({
            url: url.href,
            signal: config.signal,
            resolve: resolveRead,
          })
          if (reads.length === 1) resolveRead([])
        })
      }
      api.defaults.adapter = adapter
      let oldRefresh: Promise<unknown> | undefined
      let handoff: Promise<unknown> | undefined
      try {
        const hook = renderHook(
          () => ({
            active: useActiveRequests(scopedOptions),
            logs: useQuery({
              queryKey: logKey,
              queryFn: async ({ signal }) =>
                (
                  await fetchLogsByCategory({
                    logCategory: 'common',
                    isAdmin,
                    page: 1,
                    pageSize: 20,
                    searchParams: scopedOptions.searchParams,
                    columnFilters: [],
                    signal,
                  })
                ).data,
            }),
          }),
          { wrapper }
        )
        await waitFor(() => {
          expect(hook.result.current.active.data?.total).toBe(1)
          expect(hook.result.current.logs.data?.items).toEqual([])
        })

        act(() => {
          oldRefresh = hook.result.current.logs.refetch()
        })
        await waitFor(() => expect(reads).toHaveLength(2))
        activeItems = []
        act(() => {
          handoff = hook.result.current.active.refetch()
        })

        await waitFor(() => expect(reads).toHaveLength(3))
        expect(reads[2].url).toBe(reads[1].url)
        expect(reads[1].signal?.aborted).toBe(true)
        expect(hook.result.current.active.data?.items).toEqual([activeRequest])
        expect(hook.result.current.logs.data?.items).toEqual([])

        await act(async () => {
          reads[2].resolve([persistedLog])
          await handoff
        })
        await waitFor(() => {
          expect(hook.result.current.active.data?.items).toEqual([])
          expect(hook.result.current.logs.data?.items).toEqual([persistedLog])
        })
        await act(async () => {
          reads[1].resolve([])
          await oldRefresh
        })
        expect(hook.result.current.logs.data?.items).toEqual([persistedLog])
      } finally {
        await act(async () => {
          for (const read of reads) read.resolve([])
          await Promise.all([oldRefresh, handoff])
        })
        api.defaults.adapter = originalAdapter
      }
    }
  )

  it.each([false, true])(
    'waits for persisted logs before dropping an ended request (refresh failure: %s)',
    async (refreshFails) => {
      const logKey = ['logs', 'common', 'self', 1, 20, {}, 7]
      const otherUserKey = ['logs', 'common', 'self', 1, 20, {}, 8]
      const drawingKey = ['logs', 'drawing', 'self', 1, 20, {}, 7]
      client.setQueryData(otherUserKey, [])
      client.setQueryData(drawingKey, [])
      let activeItems = [activeRequest]
      let resolveLogs!: (value: { data: (typeof persistedLog)[] }) => void
      let rejectLogs!: (reason: Error) => void
      const get = vi.spyOn(api, 'get').mockImplementation((url) => {
        if (url.startsWith('/api/log/self/active?')) {
          return Promise.resolve(activeResponse(activeItems))
        }
        if (url === '/api/log/self') return Promise.resolve({ data: [] })
        throw new Error(`Unexpected GET ${url}`)
      })
      const hook = renderHook(
        () => ({
          active: useActiveRequests(options),
          logs: useQuery({
            queryKey: logKey,
            queryFn: async () =>
              (await api.get<(typeof persistedLog)[]>('/api/log/self')).data,
          }),
        }),
        { wrapper }
      )
      await waitFor(() => {
        expect(hook.result.current.active.data?.total).toBe(1)
        expect(hook.result.current.logs.data).toEqual([])
      })
      get.mockImplementation((url) => {
        if (url.startsWith('/api/log/self/active?')) {
          return Promise.resolve(activeResponse(activeItems))
        }
        return new Promise((resolve, reject) => {
          resolveLogs = resolve
          rejectLogs = reject
        })
      })
      activeItems = []
      let refresh!: ReturnType<typeof hook.result.current.active.refetch>

      act(() => {
        refresh = hook.result.current.active.refetch()
      })

      await waitFor(() =>
        expect(hook.result.current.logs.isFetching).toBe(true)
      )
      expect(hook.result.current.active.data?.items).toEqual([activeRequest])
      expect(client.getQueryState(otherUserKey)?.isInvalidated).toBe(false)
      expect(client.getQueryState(drawingKey)?.isInvalidated).toBe(false)
      await act(async () => {
        if (refreshFails) rejectLogs(new Error('Logs unavailable'))
        else resolveLogs({ data: [persistedLog] })
        await refresh
      })

      await waitFor(() => {
        if (refreshFails) {
          expect(hook.result.current.active.isError).toBe(true)
          expect(hook.result.current.active.data?.items).toEqual([
            activeRequest,
          ])
        } else {
          expect(hook.result.current.active.data?.items).toEqual([])
          expect(
            mergeActiveLogs(
              hook.result.current.logs.data ?? [],
              hook.result.current.active.data
            )
          ).toEqual([persistedLog])
        }
      })
    }
  )
})
