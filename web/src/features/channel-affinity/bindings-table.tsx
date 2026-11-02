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
import { getCoreRowModel, useReactTable } from '@tanstack/react-table'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  DataTableCardGrid,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Checkbox } from '@/components/ui/checkbox'
import { useIsMobile } from '@/hooks/use-mobile'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestampToDate } from '@/lib/format'

import type { AffinityBinding } from './types'

interface Props {
  bindings: AffinityBinding[]
  selected: AffinityBinding[]
  disabled: boolean
  onSelect: (bindings: AffinityBinding[]) => void
}

export function BindingsTable(props: Props) {
  const { t, i18n } = useTranslation()
  const isMobile = useIsMobile()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const selectable = props.bindings.filter(
    (binding) => !binding.shared && binding.user_id > 0 && binding.token_id > 0
  )
  const allSelected =
    selectable.length > 0 &&
    selectable.every((binding) =>
      props.selected.some((selected) => selected.id === binding.id)
    )
  const columns: StaticDataTableColumn<AffinityBinding>[] = [
    {
      id: 'select',
      header: (
        <Checkbox
          aria-label={t('Select current page')}
          checked={allSelected}
          indeterminate={!allSelected && props.selected.length > 0}
          disabled={props.disabled || selectable.length === 0}
          onCheckedChange={(checked) =>
            props.onSelect(checked ? selectable : [])
          }
        />
      ),
      className: 'w-10',
      cell: (binding) => (
        <Checkbox
          aria-label={t('Select session {{fingerprint}}', {
            fingerprint: binding.key_fp,
          })}
          checked={props.selected.some(
            (selected) => selected.id === binding.id
          )}
          disabled={props.disabled}
          onCheckedChange={(checked) =>
            props.onSelect(
              checked
                ? [...props.selected, binding]
                : props.selected.filter(
                    (selected) => selected.id !== binding.id
                  )
            )
          }
        />
      ),
    },
    {
      id: 'owner',
      header: t('User / Token'),
      className: 'min-w-36',
      cell: (binding) => {
        if (binding.shared) {
          return <Badge variant='secondary'>{t('Shared binding')}</Badge>
        }
        if (!binding.ownership_known || binding.user_id <= 0) {
          return <Badge variant='outline'>{t('Unknown owner')}</Badge>
        }
        return (
          <div className='grid max-w-56 gap-1 break-words whitespace-normal'>
            <span>
              {binding.user_name || t('User')} #{binding.user_id}
            </span>
            <span className='text-muted-foreground text-xs'>
              {binding.token_name || t('Token')} #{binding.token_id}
            </span>
          </div>
        )
      },
    },
    {
      id: 'session',
      header: t('Session'),
      className: 'min-w-48',
      cell: (binding) => (
        <div className='grid max-w-64 gap-1 break-words whitespace-normal'>
          <span>{binding.models.join(', ') || '-'}</span>
          <span className='text-muted-foreground text-xs'>
            {binding.rule_name} · {binding.using_group}
          </span>
          <span className='text-muted-foreground font-mono text-xs'>
            {binding.key_fp}
          </span>
        </div>
      ),
    },
    {
      id: 'channel',
      header: t('Current channel'),
      className: 'min-w-32',
      cell: (binding) => (
        <span className='block max-w-48 break-words whitespace-normal'>
          {binding.channel_name || t('Channel')} #{binding.channel_id}
        </span>
      ),
    },
    {
      id: 'lifetime',
      header: t('Binding lifetime'),
      className: 'min-w-40',
      cell: (binding) => (
        <div className='grid gap-1'>
          <span title={formatTimestampToDate(binding.expires_at)}>
            {t('Expires in {{seconds}} s', {
              seconds: formatNumber(
                Math.max(0, Math.ceil(binding.expires_at - Date.now() / 1000)),
                locale
              ),
            })}
          </span>
          <span className='text-muted-foreground text-xs'>
            {binding.session_mode === 'strict'
              ? t('Strict affinity')
              : t('Prefer affinity')}
          </span>
        </div>
      ),
    },
  ]

  const table = useReactTable({
    data: props.bindings,
    columns: columns.map((column) => ({ id: column.id })),
    getCoreRowModel: getCoreRowModel(),
    getRowId: (binding) => binding.id,
    state: {
      rowSelection: Object.fromEntries(
        props.selected.map((binding) => [binding.id, true])
      ),
    },
    autoResetAll: false,
  })

  if (isMobile) {
    return (
      <div className='space-y-2' aria-label={t('Active session bindings')}>
        <div className='flex items-center gap-2 px-1 text-xs'>
          {columns[0].header}
          <span>{t('Select current page')}</span>
        </div>
        <DataTableCardGrid
          table={table}
          gridClassName='grid grid-cols-1 gap-2'
          renderCard={(row) => (
            <div className='grid min-w-0 gap-3'>
              <div className='flex items-start gap-3'>
                <span className='pt-1'>
                  {columns[0].cell?.(row.original, row.index)}
                </span>
                {columns[1].cell?.(row.original, row.index)}
              </div>
              <div className='grid min-w-0 gap-2'>
                {[columns[3], columns[2], columns[4]].map((column) => (
                  <div key={column.id} className='grid min-w-0 gap-1'>
                    <span className='text-muted-foreground text-xs'>
                      {column.header}
                    </span>
                    {column.cell?.(row.original, row.index)}
                  </div>
                ))}
              </div>
            </div>
          )}
        />
      </div>
    )
  }

  return (
    <StaticDataTable
      columns={columns}
      data={props.bindings}
      getRowKey={(binding) => binding.id}
      tableProps={{ 'aria-label': t('Active session bindings') }}
      className='max-w-full overflow-x-auto'
    />
  )
}
