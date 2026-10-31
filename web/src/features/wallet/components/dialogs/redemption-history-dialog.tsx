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
import type { ColumnDef, PaginationState } from '@tanstack/react-table'
import { Gift } from 'lucide-react'
import { useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTablePagination,
  DataTableView,
  useDataTable,
} from '@/components/data-table'
import { Dialog } from '@/components/dialog'
import { ErrorState } from '@/components/error-state'
import { Skeleton } from '@/components/ui/skeleton'
import { toIntlLocale } from '@/i18n/languages'
import { formatQuotaWithCurrency } from '@/lib/currency'
import { formatNumber, formatTimestampToDate } from '@/lib/format'
import {
  getServerErrorMessage,
  requireServerSuccess,
} from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import { getRedemptionHistory } from '../../api'
import type { RedemptionRecord } from '../../types'

interface RedemptionHistoryDialogProps {
  open: boolean
  onOpenChange: (open: boolean) => void
}

const EMPTY_RECORDS: RedemptionRecord[] = []

export function RedemptionHistoryDialog(props: RedemptionHistoryDialogProps) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const userId = useAuthStore((state) => state.auth.user?.id)
  const [pagination, setPagination] = useState<PaginationState>({
    pageIndex: 0,
    pageSize: 10,
  })
  const history = useQuery({
    queryKey: ['wallet', 'redemption-history', userId, pagination],
    queryFn: async () => {
      const response = requireServerSuccess(
        await getRedemptionHistory(
          pagination.pageIndex + 1,
          pagination.pageSize
        )
      )
      return response.data
    },
    enabled: props.open && userId != null,
    staleTime: 0,
    meta: { errorToast: false },
  })
  const columns = useMemo<ColumnDef<RedemptionRecord>[]>(
    () => [
      {
        accessorKey: 'redeemed_time',
        header: t('Redeemed at'),
        cell: ({ row }) => (
          <span className='text-muted-foreground text-xs sm:text-sm'>
            {formatTimestampToDate(row.original.redeemed_time)}
          </span>
        ),
      },
      {
        accessorKey: 'quota',
        header: t('Credited amount'),
        cell: ({ row }) => (
          <span className='font-semibold tabular-nums'>
            {formatQuotaWithCurrency(row.original.quota, {
              abbreviate: false,
              digitsLarge: 2,
              digitsSmall: 4,
              locale,
            })}
          </span>
        ),
      },
    ],
    [locale, t]
  )
  const { table } = useDataTable({
    data: history.data?.items ?? EMPTY_RECORDS,
    columns,
    totalCount: history.data?.total ?? 0,
    pagination,
    onPaginationChange: setPagination,
    manualPagination: true,
    enableSorting: false,
    getRowId: (row) => row.id.toString(),
  })

  return (
    <Dialog
      open={props.open}
      onOpenChange={props.onOpenChange}
      title={t('Redemption History')}
      description={t('View your redemption records and credited amounts')}
      bodyClassName='flex flex-col gap-4'
    >
      {history.isError ? (
        <ErrorState
          title={t('Failed to load redemption history')}
          description={getServerErrorMessage(history.error)}
          onRetry={() => void history.refetch()}
        />
      ) : (
        <>
          <dl className='bg-muted/30 grid grid-cols-2 gap-4 rounded-xl border p-4'>
            <div className='flex min-w-0 flex-col gap-1.5'>
              <dt className='text-muted-foreground text-xs'>
                {t('Total redemptions')}
              </dt>
              <dd className='text-xl font-semibold tabular-nums'>
                {history.isPending ? (
                  <Skeleton className='h-7 w-16' />
                ) : (
                  formatNumber(history.data?.total ?? 0, locale)
                )}
              </dd>
            </div>
            <div className='flex min-w-0 flex-col gap-1.5'>
              <dt className='text-muted-foreground text-xs'>
                {t('Total credited')}
              </dt>
              <dd className='text-xl font-semibold break-all tabular-nums'>
                {history.isPending ? (
                  <Skeleton className='h-7 w-24' />
                ) : (
                  formatQuotaWithCurrency(history.data?.total_quota ?? 0, {
                    abbreviate: false,
                    digitsLarge: 2,
                    digitsSmall: 4,
                    locale,
                  })
                )}
              </dd>
            </div>
          </dl>
          <DataTableView
            table={table}
            isLoading={history.isPending}
            emptyTitle={t('No redemption records yet')}
            emptyDescription={t('Your redeemed codes will appear here')}
            emptyIcon={<Gift aria-hidden='true' />}
          />
          {(history.data?.total ?? 0) > pagination.pageSize && (
            <DataTablePagination table={table} compact />
          )}
        </>
      )}
    </Dialog>
  )
}
