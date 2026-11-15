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
import type { ColumnDef } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import { TruncatedCell } from '@/components/data-table'
import { StatusBadge } from '@/components/status-badge'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { ApiKeyGroupCell } from '@/features/keys/components/api-key-group-cell'
import { API_KEY_STATUSES } from '@/features/keys/constants'
import { useMediaQuery } from '@/hooks'

import type { AdminApiKey } from '../types'

export function useAdminApiKeysColumns(
  onEdit: (keys: AdminApiKey[]) => void
): ColumnDef<AdminApiKey>[] {
  const { t } = useTranslation()
  const reducedMotion = useMediaQuery('(prefers-reduced-motion: reduce)')

  return [
    {
      id: 'select',
      header: ({ table }) => (
        <Checkbox
          checked={table.getIsAllPageRowsSelected()}
          indeterminate={table.getIsSomePageRowsSelected()}
          onCheckedChange={(checked) =>
            table.toggleAllPageRowsSelected(!!checked)
          }
          aria-label={t('Select all')}
        />
      ),
      cell: ({ row }) => (
        <Checkbox
          checked={row.getIsSelected()}
          disabled={!row.getCanSelect()}
          onCheckedChange={(checked) => row.toggleSelected(!!checked)}
          aria-label={t('Select row')}
        />
      ),
      enableHiding: false,
      size: 40,
    },
    {
      accessorKey: 'name',
      header: t('Name'),
      cell: ({ row }) => (
        <div className='min-w-0'>
          <TruncatedCell className='max-w-56 font-medium'>
            {row.original.name || `#${row.original.id}`}
          </TruncatedCell>
          <span className='text-muted-foreground text-xs'>
            #{row.original.id}
          </span>
        </div>
      ),
      meta: { mobileTitle: true },
      size: 200,
    },
    {
      accessorKey: 'username',
      header: t('User'),
      cell: ({ row }) => (
        <div className='min-w-0'>
          <TruncatedCell className='max-w-56'>
            {row.original.username || `#${row.original.user_id}`}
          </TruncatedCell>
          <TruncatedCell className='text-muted-foreground max-w-56 text-xs'>
            #{row.original.user_id}
            {row.original.display_name && ` · ${row.original.display_name}`}
          </TruncatedCell>
        </div>
      ),
      size: 200,
    },
    {
      accessorKey: 'key',
      header: t('API Key'),
      cell: ({ row }) => (
        <TruncatedCell className='max-w-full font-mono text-xs'>
          {row.original.key}
        </TruncatedCell>
      ),
      size: 230,
    },
    {
      accessorKey: 'group',
      header: t('Group'),
      cell: ({ row }) => (
        <ApiKeyGroupCell
          group={row.original.group}
          autoGroups={row.original.auto_groups}
          crossGroupRetry={row.original.cross_group_retry}
          shouldReduceMotion={reducedMotion}
        />
      ),
      size: 240,
    },
    {
      accessorKey: 'status',
      header: t('Status'),
      cell: ({ row }) => {
        const status = API_KEY_STATUSES[row.original.status]
        return status ? (
          <StatusBadge
            label={t(status.label)}
            variant={status.variant}
            copyable={false}
          />
        ) : null
      },
      meta: { mobileBadge: true },
      size: 100,
    },
    {
      id: 'actions',
      header: t('Actions'),
      cell: ({ row }) => (
        <Button
          variant='outline'
          size='sm'
          disabled={!row.original.editable}
          onClick={() => onEdit([row.original])}
        >
          {t('Change group')}
        </Button>
      ),
      enableHiding: false,
      meta: { pinned: 'right' },
      size: 130,
    },
  ]
}
