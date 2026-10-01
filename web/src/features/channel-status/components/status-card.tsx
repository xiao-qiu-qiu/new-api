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
import { Layers3, Radio } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import {
  getProbeStatus,
  getWindowAvailability,
  statusTextColors,
} from '../lib/status'
import type { PublicChannelStatus } from '../types'
import { ProbeHistory } from './probe-history'
import { ProbeStatusBadge } from './probe-status-badge'

export function StatusCard(props: { probe: PublicChannelStatus }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const probe = props.probe
  const status = getProbeStatus(probe)
  const availability = getWindowAvailability(probe)
  const ttft = probe.latest?.ttft_ms
  const ScopeIcon = probe.scope === 'group' ? Layers3 : Radio
  const time = new Intl.DateTimeFormat(locale, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  })

  return (
    <article
      className='model-status-row group bg-card/80 relative min-w-0 overflow-hidden rounded-2xl border px-4 py-3 shadow-xs backdrop-blur-sm transition-shadow hover:shadow-md sm:px-6'
      data-status={status}
    >
      <div className='relative grid min-w-0 items-center gap-x-6 gap-y-3 lg:grid-cols-[minmax(170px,1fr)_210px_minmax(280px,1.8fr)] xl:grid-cols-[minmax(200px,1fr)_220px_minmax(340px,2fr)]'>
        <div className='min-w-0'>
          <div className='min-w-0 space-y-1.5'>
            <h2 className='text-[15px] font-semibold tracking-tight break-words'>
              {probe.model}
            </h2>
            <div className='text-muted-foreground flex min-w-0 items-center gap-1.5 text-xs'>
              <ScopeIcon className='size-3.5 shrink-0' />
              <span className='truncate' title={probe.public_name}>
                {probe.public_name}
              </span>
              <span className='shrink-0 text-[10px] opacity-70'>
                /{' '}
                {probe.scope === 'group'
                  ? t('Group probe')
                  : t('Upstream probe')}
              </span>
            </div>
            <p className='text-muted-foreground text-[11px]'>
              {probe.latest
                ? time.format(new Date(probe.latest.checked_at * 1000))
                : t('No measurements yet')}
              {probe.latest && (
                <span className='ml-2'>
                  {probe.latest.source === 'active'
                    ? t('Active probe')
                    : t('Passive traffic')}
                </span>
              )}
              {probe.stale && probe.latest && (
                <span className='ml-2'>{t('Stale')}</span>
              )}
            </p>
          </div>
        </div>
        <div className='space-y-1.5'>
          <div className='flex items-center pl-3.5'>
            <ProbeStatusBadge status={status} showDot />
          </div>
          <dl className='grid grid-cols-2 gap-2.5'>
            <Tooltip>
              <TooltipTrigger
                render={
                  <div
                    tabIndex={0}
                    className='border-border/65 bg-background/60 focus-visible:ring-ring rounded-xl border px-3.5 py-2 outline-none focus-visible:ring-2'
                  />
                }
              >
                <dd
                  className={cn(
                    'text-lg leading-none font-semibold tracking-tight tabular-nums',
                    statusTextColors[status]
                  )}
                >
                  {ttft == null || probe.stale ? (
                    '—'
                  ) : (
                    <>
                      {formatNumber(ttft / 1000, locale)}
                      <span className='ml-1 text-xs font-normal'>s</span>
                    </>
                  )}
                </dd>
                <dt className='text-muted-foreground mt-1.5 text-[11px]'>
                  {t('First-token latency')}
                </dt>
              </TooltipTrigger>
              <TooltipContent>
                {t(
                  'Green below {{green}} s; yellow below {{yellow}} s; red above. Timeouts are dark red.',
                  {
                    green: probe.green_ms / 1000,
                    yellow: probe.yellow_ms / 1000,
                  }
                )}
              </TooltipContent>
            </Tooltip>
            <Tooltip>
              <TooltipTrigger
                render={
                  <div
                    tabIndex={0}
                    className='border-border/65 bg-background/60 focus-visible:ring-ring rounded-xl border px-3.5 py-2 outline-none focus-visible:ring-2'
                  />
                }
              >
                <dd
                  className={cn(
                    'text-lg leading-none font-semibold tracking-tight tabular-nums',
                    availability === null
                      ? 'text-muted-foreground'
                      : 'text-foreground'
                  )}
                >
                  {availability === null ? (
                    '—'
                  ) : (
                    <>
                      {formatNumber(availability, locale)}
                      <span className='ml-0.5 text-xs font-normal'>%</span>
                    </>
                  )}
                </dd>
                <dt className='text-muted-foreground mt-1.5 text-[11px]'>
                  {t('Availability')}
                </dt>
              </TooltipTrigger>
              <TooltipContent>
                {t(
                  '{{success}} successful requests out of {{count}} samples in this window. Unsampled periods are not counted.',
                  {
                    success: formatNumber(probe.success_count, locale),
                    count: formatNumber(probe.sample_count, locale),
                  }
                )}
              </TooltipContent>
            </Tooltip>
          </dl>
        </div>
        <div className='border-border/60 min-w-0 border-t pt-3 lg:border-0 lg:pt-0'>
          <ProbeHistory
            timeline={probe.timeline}
            start={probe.window_start}
            end={probe.window_end}
          />
        </div>
      </div>
    </article>
  )
}
