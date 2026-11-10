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
import { hashKey, useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import type {
  ColumnDef,
  OnChangeFn,
  PaginationState,
} from '@tanstack/react-table'
import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTablePage,
  DataTableRow,
  useDataTable,
} from '@/components/data-table'
import { Button } from '@/components/ui/button'
import {
  getAdminPlans,
  getSelfSubscriptionFull,
} from '@/features/subscriptions/api'
import { useMediaQuery } from '@/hooks'
import { useTableUrlState } from '@/hooks/use-table-url-state'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { createServerError } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import {
  DEFAULT_LOGS_DATA,
  LOG_TYPE_ALL_VALUE,
  LOG_TYPE_ENUM,
} from '../constants'
import type { UsageLog } from '../data/schema'
import { useActiveRequests } from '../hooks/use-active-requests'
import { useLogReadingSnapshot } from '../hooks/use-log-reading-snapshot'
import { getActiveRequest, mergeActiveLogs } from '../lib/active-requests'
import { shouldShowBillingSource } from '../lib/billing-source'
import { useColumnsByCategory } from '../lib/columns'
import { parseLogOther } from '../lib/format'
import { hasLogErrorFilters, isLiveLogQuery } from '../lib/log-query'
import { fetchLogsByCategory } from '../lib/utils'
import type { GetLogsResponse, LogCategory } from '../types'
import { CommonLogsFilterBar } from './common-logs-filter-bar'
import { TaskLogsFilterBar } from './task-logs-filter-bar'
import { UsageLogsMobileList } from './usage-logs-mobile-card'
import { useLogsViewScope, type LogsViewAccess } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

const logTypeRowTint: Record<number, string> = {
  [LOG_TYPE_ENUM.ERROR]: 'bg-rose-50/40 dark:bg-rose-950/20',
  [LOG_TYPE_ENUM.REFUND]: 'bg-blue-50/30 dark:bg-blue-950/15',
}

// Warning tint for logs where a quota conversion saturated (admin-only marker).
// Takes precedence over the per-type tint since it flags a billing anomaly.
const quotaSaturationRowTint = 'bg-amber-50/60 dark:bg-amber-950/25'

function getColumnVisibilityStorageKey(
  logCategory: LogCategory,
  viewAccess: LogsViewAccess
): string {
  return `usage-logs:${logCategory}:${viewAccess}:column-visibility`
}

function deserializeLogTypeFilter(value: unknown): unknown[] {
  let values: unknown[] = []
  if (Array.isArray(value)) {
    values = value
  } else if (value) {
    values = [value]
  }
  return values.filter((item) => String(item) !== LOG_TYPE_ALL_VALUE)
}

interface UsageLogsTableProps {
  logCategory: LogCategory
}

export function UsageLogsTable({ logCategory }: UsageLogsTableProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const getColumnClassName = useCallback(
    () => (logCategory === 'common' ? 'py-2' : 'py-3.5'),
    [logCategory]
  )
  const {
    isAdminView: isAdmin,
    isRootView: isRoot,
    viewAccess,
  } = useLogsViewScope()
  const isMobile = useMediaQuery('(max-width: 640px)')
  const searchParams = route.useSearch()
  const navigate = route.useNavigate()
  const scrollContainerRef = useRef<HTMLDivElement>(null)
  const [isRefreshing, setIsRefreshing] = useState(false)
  const [cursorHistory, setCursorHistory] = useState<{
    scope: string
    pages: Record<number, string>
  }>({ scope: '', pages: {} })
  const errorSearch =
    logCategory === 'common' && hasLogErrorFilters(searchParams)
  const liveQuery = logCategory === 'common' && isLiveLogQuery(searchParams)
  const userId = useAuthStore((state) => state.auth.user?.id)
  const { data: showBillingSource = false } = useQuery({
    queryKey: ['usage-log-billing-source', isAdmin, userId],
    enabled: logCategory === 'common' && userId != null,
    queryFn: async () => {
      if (isAdmin) {
        const plansResult = await getAdminPlans()
        return shouldShowBillingSource({
          isAdmin,
          plans: plansResult.success ? plansResult.data : undefined,
          subscriptions: undefined,
        })
      }

      const selfResult = await getSelfSubscriptionFull()
      return shouldShowBillingSource({
        isAdmin,
        plans: undefined,
        subscriptions: selfResult.success
          ? selfResult.data?.subscriptions
          : undefined,
      })
    },
  })

  const {
    columnFilters,
    onColumnFiltersChange,
    pagination,
    onPaginationChange,
    ensurePageInRange,
  } = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    pagination: { defaultPage: 1, defaultPageSize: isMobile ? 20 : 100 },
    globalFilter: { enabled: false },
    columnFilters: [
      {
        columnId: 'created_at',
        searchKey: 'type',
        type: 'array' as const,
        deserialize: deserializeLogTypeFilter,
      },
      { columnId: 'model_name', searchKey: 'model', type: 'string' as const },
      { columnId: 'token_name', searchKey: 'token', type: 'string' as const },
      { columnId: 'group', searchKey: 'group', type: 'string' as const },
      ...(isAdmin
        ? [
            {
              columnId: 'channel',
              searchKey: 'channel',
              type: 'string' as const,
            },
            {
              columnId: 'username',
              searchKey: 'username',
              type: 'string' as const,
            },
          ]
        : []),
    ],
  })

  const queryKey = [
    'logs',
    logCategory,
    viewAccess,
    pagination.pageIndex + 1,
    pagination.pageSize,
    columnFilters,
    searchParams,
    userId,
  ]
  const queryScope = hashKey(queryKey)
  const [settledScope, setSettledScope] = useState(queryScope)
  const cursorScope = hashKey([
    logCategory,
    viewAccess,
    userId,
    {
      ...searchParams,
      page: undefined,
      cursor: undefined,
    },
  ])
  const [previousCursorScope, setPreviousCursorScope] = useState(cursorScope)
  const resetCursor =
    previousCursorScope !== cursorScope &&
    errorSearch &&
    (Boolean(searchParams.cursor) || pagination.pageIndex > 0)
  useLayoutEffect(() => {
    if (previousCursorScope === cursorScope) return
    if (resetCursor) {
      void navigate({
        search: (previous) => ({ ...previous, page: 1, cursor: undefined }),
      })
    } else {
      setPreviousCursorScope(cursorScope)
    }
  }, [cursorScope, previousCursorScope, resetCursor, navigate])
  const logQuery = useQuery({
    queryKey,
    enabled: !resetCursor,
    queryFn: async ({
      signal,
    }): Promise<NonNullable<GetLogsResponse['data']>> => {
      const result = await fetchLogsByCategory({
        logCategory,
        isAdmin,
        page: pagination.pageIndex + 1,
        pageSize: pagination.pageSize,
        searchParams,
        columnFilters,
        signal,
      })

      if (!result?.success) {
        throw createServerError(result, t('Failed to load logs'))
      }

      return (
        result.data ?? {
          ...DEFAULT_LOGS_DATA,
          page: pagination.pageIndex + 1,
          page_size: pagination.pageSize,
        }
      )
    },
    placeholderData: (previousData, previousQuery) => {
      if (
        previousQuery?.queryKey[1] === logCategory &&
        previousQuery.queryKey[2] === viewAccess &&
        previousQuery.queryKey.at(-1) === userId
      ) {
        return previousData
      }
      return undefined
    },
    retry: false,
    refetchInterval: (query) =>
      liveQuery && pagination.pageIndex === 0 && query.state.status !== 'error'
        ? 30_000
        : false,
    refetchIntervalInBackground: false,
  })
  const { data, isLoading, isFetching } = logQuery
  if (!isFetching && settledScope !== queryScope) setSettledScope(queryScope)

  const showActive = liveQuery && pagination.pageIndex === 0 && userId != null
  const activeQuery = useActiveRequests({
    enabled: showActive,
    isAdmin,
    viewAccess,
    userId,
    searchParams,
    columnFilters,
  })
  const active =
    showActive && !activeQuery.isError ? activeQuery.data : undefined
  const incomingLogs = useMemo(() => {
    const items = data?.items ?? DEFAULT_LOGS_DATA.items
    return logCategory === 'common'
      ? mergeActiveLogs(items as UsageLog[], active)
      : (items as UsageLog[])
  }, [data?.items, active, logCategory])
  const reading = useLogReadingSnapshot({
    logs: incomingLogs,
    scope: queryScope,
    enabled:
      logCategory === 'common' && !logQuery.isPlaceholderData && !resetCursor,
  })
  const logs = reading.logs
  useLayoutEffect(() => {
    if (scrollContainerRef.current) scrollContainerRef.current.scrollTop = 0
  }, [queryScope])

  const resumeUpdates = () => {
    reading.resume()
    if (scrollContainerRef.current) scrollContainerRef.current.scrollTop = 0
  }
  const refreshLogs = async () => {
    if (isRefreshing) return
    setIsRefreshing(true)
    try {
      const result = await logQuery.refetch({ cancelRefetch: false })
      if (!result.isError) resumeUpdates()
    } finally {
      setIsRefreshing(false)
    }
  }
  const changePage: OnChangeFn<PaginationState> = (update) => {
    if (!errorSearch) {
      onPaginationChange(update)
      return
    }
    const next = typeof update === 'function' ? update(pagination) : update
    const pages = cursorHistory.scope === cursorScope ? cursorHistory.pages : {}
    let pageIndex = next.pageIndex
    let cursor: string | undefined
    if (next.pageSize !== pagination.pageSize) pageIndex = 0
    if (pageIndex > 0) {
      cursor =
        pageIndex === pagination.pageIndex + 1
          ? data?.next_cursor
          : pages[pageIndex]
      if (!cursor || isFetching) return
    }
    setCursorHistory({
      scope: cursorScope,
      pages: {
        ...pages,
        [pagination.pageIndex]: searchParams.cursor ?? '',
        [pageIndex]: cursor ?? '',
      },
    })
    void navigate({
      search: (previous) => ({
        ...previous,
        page: pageIndex + 1,
        pageSize: next.pageSize,
        cursor,
      }),
    })
  }
  const columns = useColumnsByCategory(
    logCategory,
    isAdmin,
    isRoot,
    showBillingSource
  )
  const isLoadingData =
    (isLoading || (isFetching && !data)) && logs.length === 0

  const { table } = useDataTable({
    data: logs as Record<string, unknown>[],
    columns: columns as ColumnDef<Record<string, unknown>>[],
    columnFilters,
    columnVisibilityStorageKey: getColumnVisibilityStorageKey(
      logCategory,
      viewAccess
    ),
    pagination,
    enableRowSelection: false,
    getRowId: (row, index) => {
      const request = getActiveRequest(row as UsageLog)
      return request
        ? `active:${request.request_id}`
        : `log:${row.request_id ?? ''}:${row.id ?? index}`
    },
    onPaginationChange: changePage,
    onColumnFiltersChange,
    manualPagination: true,
    manualFiltering: true,
    totalCount: data?.total || 0,
    pageCount: errorSearch
      ? pagination.pageIndex + 1 + (data?.has_more ? 1 : 0)
      : undefined,
    ensurePageInRange: errorSearch ? undefined : ensurePageInRange,
  })

  const isCommon = logCategory === 'common'

  return (
    <DataTablePage
      table={table}
      compactPagination={isMobile && isCommon}
      paginationTotalExact={!errorSearch}
      paginationHasMore={errorSearch ? (data?.has_more ?? false) : undefined}
      paginationDisabled={errorSearch && isFetching}
      paginationCanPrevious={
        errorSearch
          ? pagination.pageIndex > 0 &&
            (pagination.pageIndex === 1 ||
              (cursorHistory.scope === cursorScope &&
                cursorHistory.pages[pagination.pageIndex - 1] !== undefined))
          : undefined
      }
      columns={columns as ColumnDef<Record<string, unknown>>[]}
      isLoading={isLoadingData}
      isFetching={isFetching}
      dimOnRefetch={!isCommon}
      tableLayout={isCommon ? 'fixed' : 'auto'}
      scrollContainerRef={scrollContainerRef}
      onScroll={(event) => {
        if (event.currentTarget.scrollTop > 24) reading.pause()
        else reading.resume()
      }}
      emptyTitle={t('No Logs Found')}
      emptyDescription={t(
        'No usage logs available. Logs will appear here once API calls are made.'
      )}
      skeletonKeyPrefix='usage-log-skeleton'
      applyHeaderSize
      tableClassName={cn(
        '[&_[data-slot=table]]:text-[13px] [&_[data-slot=table]_td]:text-[13px] [&_[data-slot=table]_td_*]:text-[13px] [&_[data-slot=table]_th]:text-[13px] [&_[data-slot=table]_th_*]:text-[13px]',
        isCommon && '[&_td]:overflow-hidden'
      )}
      mobile={
        <UsageLogsMobileList
          table={table}
          isLoading={isLoadingData}
          logCategory={logCategory}
        />
      }
      toolbar={
        isCommon ? (
          <>
            <CommonLogsFilterBar
              table={table}
              onRefresh={refreshLogs}
              isRefreshing={
                isRefreshing || (isFetching && settledScope !== queryScope)
              }
            />
            <div className='flex h-7 shrink-0 items-center justify-between gap-2 overflow-hidden'>
              <div role='status' className='min-w-0 truncate text-xs'>
                {showActive && activeQuery.isError && (
                  <span className='text-warning'>
                    {t('Live request status is temporarily unavailable')}
                  </span>
                )}
                {!activeQuery.isError && active != null && active.total > 0 && (
                  <span className='text-info'>
                    {active.total > active.items.length
                      ? t(
                          'Showing {{shown}} of {{total}} in-progress requests',
                          {
                            shown: formatNumber(active.items.length, locale),
                            total: formatNumber(active.total, locale),
                          }
                        )
                      : t('In progress: {{total}}', {
                          total: formatNumber(active.total, locale),
                        })}
                  </span>
                )}
              </div>
              {reading.hasUpdates && (
                <Button
                  variant='ghost'
                  size='sm'
                  className='h-7 shrink-0'
                  onClick={resumeUpdates}
                >
                  {t('New logs available')}
                </Button>
              )}
            </div>
          </>
        ) : (
          <TaskLogsFilterBar table={table} logCategory={logCategory} />
        )
      }
      renderRow={(row) => {
        const logType = (row.original as Record<string, unknown>).type as
          | number
          | undefined
        let tintClass =
          isCommon && logType != null ? (logTypeRowTint[logType] ?? '') : ''
        if (isCommon && getActiveRequest(row.original as UsageLog)) {
          tintClass = 'bg-info/5'
        }
        if (isCommon && isAdmin) {
          const other = parseLogOther(
            ((row.original as Record<string, unknown>).other as string) ?? ''
          )
          if (other?.admin_info?.quota_saturation) {
            tintClass = quotaSaturationRowTint
          }
        }

        return (
          <DataTableRow
            key={row.id}
            row={row}
            className={cn('transition-colors', tintClass)}
            getColumnClassName={getColumnClassName}
            cellRenderColumns={table.options.columns}
          />
        )
      }}
    />
  )
}
