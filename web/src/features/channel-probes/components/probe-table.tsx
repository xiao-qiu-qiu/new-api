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
import { ArrowDown, ArrowUp, Layers3, Play, Radio } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  StaticDataTable,
  StaticRowActions,
  type StaticDataTableColumn,
} from '@/components/data-table'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { ProbeStatusBadge } from '@/features/channel-status/components/probe-status-badge'
import { getProbeStatus } from '@/features/channel-status/lib/status'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'

import type { ChannelProbe } from '../types'

export function ProbeTable(props: {
  probes: ChannelProbe[]
  busy: boolean
  runningId?: number
  onEdit: (probe: ChannelProbe) => void
  onDelete: (probe: ChannelProbe) => void
  onRun: (probe: ChannelProbe) => void
  onMove: (probe: ChannelProbe, direction: -1 | 1) => void
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const columns: StaticDataTableColumn<ChannelProbe>[] = [
    {
      id: 'order',
      header: t('Order'),
      cell: (probe) => (
        <div className='flex items-center gap-1'>
          <span className='text-muted-foreground mr-1 min-w-4 text-xs tabular-nums'>
            {probe.sort_order}
          </span>
          <div className='flex flex-col'>
            <Button
              variant='ghost'
              size='icon'
              className='size-6'
              aria-label={t('Move {{name}} up', { name: probe.public_name })}
              disabled={props.busy || props.probes[0]?.id === probe.id}
              onClick={() => props.onMove(probe, -1)}
            >
              <ArrowUp className='size-3' />
            </Button>
            <Button
              variant='ghost'
              size='icon'
              className='size-6'
              aria-label={t('Move {{name}} down', { name: probe.public_name })}
              disabled={props.busy || props.probes.at(-1)?.id === probe.id}
              onClick={() => props.onMove(probe, 1)}
            >
              <ArrowDown className='size-3' />
            </Button>
          </div>
        </div>
      ),
    },
    {
      id: 'name',
      header: t('Probe'),
      cell: (probe) => (
        <div className='max-w-64 space-y-1'>
          <div className='font-medium break-words'>
            {probe.public_name || t('Unnamed probe')}
          </div>
          <div className='text-muted-foreground flex items-center gap-1.5 text-xs'>
            {probe.scope === 'group' ? (
              <>
                <Layers3 className='size-3' />
                {t('Group probe')} · {probe.group_name}
              </>
            ) : (
              <>
                <Radio className='size-3' />
                {t('Channel #{{id}}', { id: probe.channel_id })}
              </>
            )}
          </div>
        </div>
      ),
    },
    {
      id: 'model',
      header: t('Models'),
      cell: (probe) => (
        <div className='flex max-w-56 flex-col items-start gap-1.5'>
          {(probe.models?.length ? probe.models : [probe.model]).map(
            (model) => (
              <Badge
                key={model}
                variant='secondary'
                className='h-5 max-w-full truncate font-mono text-[11px]'
                title={model}
              >
                {model}
              </Badge>
            )
          )}
        </div>
      ),
    },
    {
      id: 'status',
      header: t('Status'),
      cell: (probe) => (
        <div className='flex flex-wrap items-center gap-1.5'>
          <div className='flex flex-col gap-1.5'>
            {(probe.model_statuses ?? [probe]).map((item) => (
              <Tooltip key={item.model}>
                <TooltipTrigger
                  render={<span tabIndex={0} className='inline-flex' />}
                >
                  <ProbeStatusBadge status={getProbeStatus(item)} />
                </TooltipTrigger>
                <TooltipContent>{item.model}</TooltipContent>
              </Tooltip>
            ))}
          </div>
          {!probe.enabled && <Badge variant='outline'>{t('Disabled')}</Badge>}
        </div>
      ),
    },
    {
      id: 'ttft',
      header: t('First-token latency'),
      cell: (probe) => (
        <div className='flex flex-col gap-1.5 text-xs tabular-nums'>
          {(probe.model_statuses ?? [probe]).map((item) => (
            <span className='leading-5' key={item.model} title={item.model}>
              {item.latest?.ttft_ms == null || item.stale
                ? '—'
                : `${formatNumber(item.latest.ttft_ms / 1000, locale)} s`}
            </span>
          ))}
        </div>
      ),
    },
    {
      id: 'sources',
      header: t('Collection'),
      cell: (probe) => (
        <div className='flex flex-wrap gap-1'>
          {probe.active_enabled && (
            <Badge variant='secondary'>{t('Active')}</Badge>
          )}
          {probe.passive_enabled && (
            <Badge variant='secondary'>{t('Passive')}</Badge>
          )}
          {!probe.active_enabled && !probe.passive_enabled && (
            <span className='text-muted-foreground'>—</span>
          )}
        </div>
      ),
    },
    {
      id: 'public',
      header: t('Visibility'),
      cell: (probe) => (
        <Badge variant={probe.public ? 'secondary' : 'outline'}>
          {probe.public ? t('Public') : t('Private')}
        </Badge>
      ),
    },
    {
      id: 'interval',
      header: t('Interval'),
      cell: (probe) =>
        t('{{seconds}} s', {
          seconds: formatNumber(probe.interval_seconds, locale),
        }),
    },
    {
      id: 'actions',
      header: <span className='sr-only'>{t('Actions')}</span>,
      cell: (probe) => (
        <div className='flex items-center justify-end gap-2'>
          <Tooltip>
            <TooltipTrigger render={<span tabIndex={0} />}>
              <Button
                size='sm'
                variant='outline'
                disabled={props.busy || !probe.enabled || !probe.active_enabled}
                onClick={() => props.onRun(probe)}
              >
                <Play className='size-3.5' />
                {props.runningId === probe.id
                  ? t('Queuing...')
                  : t('Probe now')}
              </Button>
            </TooltipTrigger>
            <TooltipContent>
              {t(
                'Queues an active probe. Enable the probe and active collection first.'
              )}
            </TooltipContent>
          </Tooltip>
          <StaticRowActions
            editLabel={t('Edit')}
            deleteLabel={t('Delete')}
            menuLabel={t('Probe actions')}
            onEdit={() => props.onEdit(probe)}
            onDelete={() => props.onDelete(probe)}
            editDisabled={props.busy}
            deleteDisabled={props.busy}
          />
        </div>
      ),
    },
  ]
  return (
    <StaticDataTable
      columns={columns}
      data={props.probes}
      getRowKey={(probe) => probe.id}
      tableClassName='min-w-[900px]'
    />
  )
}
