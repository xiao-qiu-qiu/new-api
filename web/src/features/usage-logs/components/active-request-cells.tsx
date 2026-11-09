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
import { flexRender, type Cell } from '@tanstack/react-table'
import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { CopyButton } from '@/components/copy-button'
import { StatusBadge } from '@/components/status-badge'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber, formatTimestampToDate } from '@/lib/format'

import type { ActiveUsageLog } from '../lib/active-requests'
import { useUsageLogsContext } from './usage-logs-provider'

type ObservedRequest = ActiveUsageLog['active_request']

export function ActiveRequestStatus(props: { request: ObservedRequest }) {
  const { t } = useTranslation()
  let label = t('Processing')
  let warning = false
  switch (props.request.status) {
    case 'waiting':
      label = props.request.is_stream
        ? t('Waiting for first response')
        : t('Waiting for response')
      break
    case 'streaming':
      label = t('Streaming response')
      break
    case 'retrying':
      label = t('Retrying request')
      warning = true
      break
    case 'draining':
      label = t('Finishing after disconnect')
      warning = true
      break
  }
  return (
    <StatusBadge
      label={label}
      variant={warning ? 'warning' : 'info'}
      size='sm'
      copyable={false}
    />
  )
}

export function ActiveRequestElapsed(props: { request: ObservedRequest }) {
  const { t, i18n } = useTranslation()
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    let timer: ReturnType<typeof setInterval> | undefined
    const updateVisibility = () => {
      clearInterval(timer)
      if (document.visibilityState === 'hidden') return
      setNow(Date.now())
      timer = setInterval(() => setNow(Date.now()), 1000)
    }
    updateVisibility()
    document.addEventListener('visibilitychange', updateVisibility)
    return () => {
      clearInterval(timer)
      document.removeEventListener('visibilitychange', updateVisibility)
    }
  }, [])
  const seconds = Math.max(
    0,
    Math.floor(
      (now + props.request.clock_offset_ms - props.request.started_at) / 1000
    )
  )
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  return (
    <span
      className='whitespace-nowrap tabular-nums'
      aria-label={t('Elapsed time')}
    >
      {t('{{seconds}} s elapsed', { seconds: formatNumber(seconds, locale) })}
    </span>
  )
}

export function ActiveRequestID(props: { request: ObservedRequest }) {
  const { t } = useTranslation()
  const { sensitiveVisible } = useUsageLogsContext()
  if (!sensitiveVisible) {
    return <span className='text-muted-foreground'>••••</span>
  }
  return (
    <CopyButton
      value={props.request.request_id}
      size='sm'
      className='h-auto max-w-full min-w-0 justify-start px-0 py-1'
      aria-label={t('Copy request ID')}
    >
      <span className='truncate font-mono text-xs'>
        {props.request.request_id}
      </span>
    </CopyButton>
  )
}

// Existing columns retain their model, user, channel and token interactions.
// Pending quantities deliberately never reach billing or token formatters.
export function ActiveRequestCell(props: {
  request: ObservedRequest
  columnId: string
  children: ReactNode
}) {
  const { t } = useTranslation()
  switch (props.columnId) {
    case 'created_at':
      return (
        <div className='flex min-w-0 flex-col items-start gap-0.5'>
          <span className='truncate font-mono text-xs tabular-nums'>
            {formatTimestampToDate(props.request.started_at / 1000)}
          </span>
          <ActiveRequestStatus request={props.request} />
        </div>
      )
    case 'use_time':
      return <ActiveRequestElapsed request={props.request} />
    case 'content':
      return <ActiveRequestID request={props.request} />
    case 'prompt_tokens':
    case 'quota':
      return (
        <span
          className='text-muted-foreground'
          aria-label={t('Not yet available')}
        >
          —
        </span>
      )
    case 'channel':
      if (!props.request.channel) {
        return <span className='text-muted-foreground'>—</span>
      }
      return props.children
    default:
      return props.children
  }
}

export function ActiveRequestMobileCard<TData>(props: {
  request: ObservedRequest
  cells: Map<string, Cell<TData, unknown>>
}) {
  const { t } = useTranslation()
  const model = props.cells.get('model_name')
  const metadata = [
    { id: 'user', label: t('User') },
    { id: 'channel', label: t('Channel') },
    { id: 'token_name', label: t('Token') },
  ]
  return (
    <div className='min-w-0 space-y-2.5 text-sm'>
      <div className='flex min-w-0 flex-wrap items-center justify-between gap-2'>
        <div className='min-w-0 overflow-hidden'>
          {model && flexRender(model.column.columnDef.cell, model.getContext())}
        </div>
        <ActiveRequestStatus request={props.request} />
      </div>
      <div className='text-muted-foreground flex flex-wrap justify-between gap-2 text-xs'>
        <span className='tabular-nums'>
          {formatTimestampToDate(props.request.started_at / 1000)}
        </span>
        <ActiveRequestElapsed request={props.request} />
      </div>
      <div className='grid min-w-0 grid-cols-2 gap-2'>
        {metadata.map((field) => {
          const cell = props.cells.get(field.id)
          if (!cell) return null
          return (
            <div key={field.id} className='min-w-0 overflow-hidden'>
              <div className='text-muted-foreground mb-1 text-xs'>
                {field.label}
              </div>
              {flexRender(cell.column.columnDef.cell, cell.getContext())}
            </div>
          )
        })}
        <div className='text-muted-foreground flex flex-wrap items-center gap-2 text-xs'>
          <span>{t('Tokens', { context: 'usage' })}: —</span>
          <span>{t('Cost')}: —</span>
        </div>
      </div>
      <ActiveRequestID request={props.request} />
    </div>
  )
}
