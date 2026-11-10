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
import { useQuery } from '@tanstack/react-query'
import { getRouteApi } from '@tanstack/react-router'
import { Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import { Skeleton } from '@/components/ui/skeleton'
import { formatLogQuota } from '@/lib/format'
import { requireServerSuccess } from '@/lib/server-error-message'
import { cn } from '@/lib/utils'
import { useAuthStore } from '@/stores/auth-store'

import { getLogStats, getUserLogStats } from '../api'
import { DEFAULT_LOG_STATS } from '../constants'
import {
  getLogQueryValidationError,
  getLogStatsParams,
  getLogStatsQueryKey,
  requiresLogScanBudget,
} from '../lib/log-query'
import { useLogsViewScope, useUsageLogsContext } from './usage-logs-provider'

const route = getRouteApi('/_authenticated/usage-logs/$section')

function StatBadge(props: {
  label: string
  value: string | number
  accent: string
}) {
  return (
    <span className='border-border/60 bg-muted/25 inline-flex h-7 items-center gap-2 rounded-md border px-2.5 text-xs shadow-xs'>
      <span className={cn('h-3.5 w-0.5 rounded-full', props.accent)} />
      <span className='text-muted-foreground'>{props.label}</span>
      <span className='text-foreground/85 font-mono font-semibold tabular-nums'>
        {props.value}
      </span>
    </span>
  )
}

export function CommonLogsStats() {
  const { t } = useTranslation()
  const {
    isAdminView: isAdmin,
    canManageScope,
    viewAccess,
  } = useLogsViewScope()
  const userId = useAuthStore((state) => state.auth.user?.id)
  const searchParams = route.useSearch()
  const { sensitiveVisible } = useUsageLogsContext()
  const manualOnly = requiresLogScanBudget(searchParams)
  const validationError = getLogQueryValidationError(
    searchParams,
    canManageScope
  )

  const {
    data: stats,
    isLoading,
    isFetching,
    refetch,
  } = useQuery({
    queryKey: getLogStatsQueryKey(searchParams, isAdmin, viewAccess, userId),
    queryFn: async ({ signal }) => {
      const params = getLogStatsParams(searchParams, isAdmin)

      const result = isAdmin
        ? requireServerSuccess(await getLogStats(params, signal))
        : requireServerSuccess(await getUserLogStats(params, signal))

      return result.success
        ? result.data || DEFAULT_LOG_STATS
        : DEFAULT_LOG_STATS
    },
    enabled: !manualOnly,
    retry: false,
    staleTime: 60_000,
    refetchOnWindowFocus: false,
    placeholderData: (previousData, previousQuery) => {
      if (
        manualOnly ||
        previousQuery?.queryKey[1] !== viewAccess ||
        previousQuery.queryKey[2] !== userId
      ) {
        return undefined
      }
      const previousParams = previousQuery.queryKey[3]
      const previousManualOnly = requiresLogScanBudget({
        model: previousParams.model_name,
        token: previousParams.token_name,
        username: previousParams.username,
        errorKeyword: previousParams.error_keyword,
        errorCode: previousParams.error_code,
        requestResult: previousParams.request_result,
        startTime:
          previousParams.start_timestamp == null
            ? undefined
            : previousParams.start_timestamp * 1000,
        endTime:
          previousParams.end_timestamp == null
            ? undefined
            : previousParams.end_timestamp * 1000,
      })
      return previousManualOnly ? undefined : previousData
    },
  })

  if (isLoading && !manualOnly) {
    return (
      <div className='flex items-center gap-2'>
        <Skeleton className='h-7 w-[150px] rounded-md' />
        <Skeleton className='h-7 w-[100px] rounded-md' />
        <Skeleton className='h-7 w-[120px] rounded-md' />
      </div>
    )
  }

  return (
    <div className='flex flex-wrap items-center gap-2'>
      {manualOnly && (
        <Button
          type='button'
          size='sm'
          variant='outline'
          disabled={isFetching || Boolean(validationError)}
          aria-busy={isFetching}
          title={validationError ? t(validationError) : undefined}
          onClick={() => {
            void refetch()
          }}
        >
          <Loader2
            aria-hidden='true'
            className={cn(
              'size-3.5',
              isFetching ? 'animate-spin' : 'invisible'
            )}
          />
          {t('Load statistics')}
        </Button>
      )}
      {(!manualOnly || stats) && (
        <>
          <StatBadge
            label={t('Usage')}
            value={
              sensitiveVisible ? formatLogQuota(stats?.quota || 0) : '••••'
            }
            accent='bg-sky-500/70'
          />
          <StatBadge
            label={t('RPM')}
            value={stats?.rpm || 0}
            accent='bg-rose-500/65'
          />
          <StatBadge
            label={t('TPM')}
            value={stats?.tpm || 0}
            accent='bg-slate-400/70'
          />
        </>
      )}
    </div>
  )
}
