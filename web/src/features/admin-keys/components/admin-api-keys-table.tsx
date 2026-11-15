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
import type { RowSelectionState } from '@tanstack/react-table'
import { RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'

import {
  DataTableBulkActions,
  DataTablePage,
  useDataTable,
  useDebouncedColumnFilter,
} from '@/components/data-table'
import { ErrorState } from '@/components/error-state'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { useTableUrlState } from '@/hooks/use-table-url-state'

import { getAdminApiKeys } from '../api'
import type { AdminApiKey } from '../types'
import { AdminApiKeyGroupDialog } from './admin-api-key-group-dialog'
import { useAdminApiKeysColumns } from './admin-api-keys-columns'

const route = getRouteApi('/_authenticated/admin/keys/')
const EMPTY_KEYS: AdminApiKey[] = []
const EMPTY_SELECTION: RowSelectionState = {}

export function AdminApiKeysTable() {
  const { t } = useTranslation()
  const [editingKeys, setEditingKeys] = useState<AdminApiKey[]>([])
  const state = useTableUrlState({
    search: route.useSearch(),
    navigate: route.useNavigate(),
    globalFilter: { enabled: true, key: 'filter' },
    columnFilters: [
      { columnId: 'group', searchKey: 'group', type: 'string' },
      { columnId: '_userId', searchKey: 'user_id', type: 'string' },
    ],
  })
  const group = useDebouncedColumnFilter({
    columnFilters: state.columnFilters,
    columnId: 'group',
    onColumnFiltersChange: state.onColumnFiltersChange,
  })
  const owner = useDebouncedColumnFilter({
    columnFilters: state.columnFilters,
    columnId: '_userId',
    onColumnFiltersChange: state.onColumnFiltersChange,
  })
  const params = {
    p: state.pagination.pageIndex + 1,
    page_size: state.pagination.pageSize,
    keyword: state.globalFilter || undefined,
    group: group.value || undefined,
    user_id: owner.value || undefined,
  }
  const query = useQuery({
    queryKey: ['admin-keys', params],
    queryFn: () => getAdminApiKeys(params),
  })
  const selectionScope = JSON.stringify(params)
  const [selection, setSelection] = useState({
    scope: selectionScope,
    rows: EMPTY_SELECTION,
  })
  if (selection.scope !== selectionScope) {
    setSelection({ scope: selectionScope, rows: EMPTY_SELECTION })
  }
  const columns = useAdminApiKeysColumns(setEditingKeys)
  const { table } = useDataTable({
    data: query.data?.items ?? EMPTY_KEYS,
    columns,
    ...state,
    enableSorting: false,
    manualFiltering: true,
    manualPagination: true,
    getRowId: (row) => String(row.id),
    enableRowSelection: (row) => row.original.editable,
    totalCount: query.data?.total ?? 0,
    rowSelection:
      selection.scope === selectionScope ? selection.rows : EMPTY_SELECTION,
    onRowSelectionChange: (updater) => {
      setSelection((previous) => {
        const rows =
          previous.scope === selectionScope ? previous.rows : EMPTY_SELECTION
        return {
          scope: selectionScope,
          rows: typeof updater === 'function' ? updater(rows) : updater,
        }
      })
    },
  })
  const selectedKeys = table
    .getFilteredSelectedRowModel()
    .rows.map((row) => row.original)

  return (
    <>
      <DataTablePage
        table={table}
        columns={columns}
        isLoading={query.isLoading}
        isFetching={query.isFetching}
        emptyTitle={
          query.isError ? t('Failed to load API keys') : t('No API Keys Found')
        }
        emptyDescription={
          query.isError ? '' : t('No API keys match the current filters.')
        }
        emptyAction={
          query.isError ? (
            <Button variant='outline' onClick={() => void query.refetch()}>
              {t('Retry')}
            </Button>
          ) : undefined
        }
        mobile={
          query.isError ? (
            <ErrorState
              title={t('Failed to load API keys')}
              onRetry={() => void query.refetch()}
            />
          ) : undefined
        }
        mobileProps={{ enableRowSelection: true }}
        showMobileBulkActions
        toolbarProps={{
          searchPlaceholder: t('Search key name, username, or user ID...'),
          searchDebounceMs: 400,
          additionalSearch: (
            <>
              <Input
                placeholder={t('User ID')}
                aria-label={t('User ID')}
                inputMode='numeric'
                value={owner.inputValue}
                onChange={(event) => {
                  if (/^\d*$/.test(event.target.value)) {
                    owner.setInputValue(event.target.value)
                  }
                }}
                className='w-full sm:w-28'
              />
              <Input
                placeholder={t('Filter by group...')}
                aria-label={t('Filter by group...')}
                value={group.inputValue}
                onChange={group.onChange}
                onCompositionStart={group.onCompositionStart}
                onCompositionEnd={group.onCompositionEnd}
                className='w-full sm:w-44'
              />
            </>
          ),
          preActions: (
            <Button
              variant='outline'
              size='icon'
              disabled={query.isFetching}
              aria-label={t('Refresh')}
              onClick={() => void query.refetch()}
            >
              <RefreshCw aria-hidden='true' className='size-4' />
            </Button>
          ),
        }}
        bulkActions={
          <DataTableBulkActions table={table} entityName={t('API Key')}>
            <Button
              size='sm'
              disabled={selectedKeys.length > 100 || query.isFetching}
              onClick={() => setEditingKeys(selectedKeys)}
            >
              {t('Change group')}
            </Button>
          </DataTableBulkActions>
        }
      />
      {editingKeys.length > 0 && (
        <AdminApiKeyGroupDialog
          keys={editingKeys}
          onClose={() => setEditingKeys([])}
          onSaved={() => table.resetRowSelection()}
        />
      )}
    </>
  )
}
