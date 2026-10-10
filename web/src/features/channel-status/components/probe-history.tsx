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
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { statusColors } from '../lib/status'
import type { ProbeTimeBucket } from '../types'

export function ProbeHistory(props: {
  timeline: ProbeTimeBucket[]
  start: number
  end: number
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const time = new Intl.DateTimeFormat(locale, {
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  })
  const fullTime = new Intl.DateTimeFormat(locale, {
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    hourCycle: 'h23',
  })
  const labels = {
    green: t('Responsive'),
    yellow: t('Elevated latency'),
    red: t('Slow or unavailable'),
    timeout: t('Timed out'),
    unknown: t('No samples'),
  }
  const acrossDays =
    new Date(props.start * 1000).toDateString() !==
    new Date(props.end * 1000).toDateString()
  const axis = acrossDays ? fullTime : time

  return (
    <div className='min-w-0 space-y-2'>
      <div className='text-muted-foreground flex items-center justify-between text-[11px] tabular-nums'>
        <span>{axis.format(new Date(props.start * 1000))}</span>
        <span>{t('Availability history')}</span>
        <span>{axis.format(new Date(props.end * 1000))}</span>
      </div>
      <div
        className='flex h-8 items-stretch gap-[2px] sm:gap-[3px]'
        role='group'
        aria-label={t('Recent probe history')}
      >
        {props.timeline.map((bucket) => {
          const period = `${fullTime.format(new Date(bucket.start_at * 1000))} – ${time.format(new Date(bucket.end_at * 1000))}`
          const label = `${period} · ${labels[bucket.status]} · ${t('{{count}} samples', { count: bucket.count })}`
          return (
            <Tooltip key={bucket.start_at}>
              <TooltipTrigger
                render={
                  <Button
                    variant='ghost'
                    className='group/bar h-8 min-w-0 flex-1 rounded-[3px] px-0 py-0 hover:bg-transparent focus-visible:z-10'
                    aria-label={label}
                  />
                }
              >
                <span
                  className={cn(
                    'h-full w-full rounded-[3px] transition-[opacity,transform] group-hover/bar:scale-y-110 group-hover/bar:opacity-80',
                    statusColors[bucket.status]
                  )}
                />
              </TooltipTrigger>
              <TooltipContent className='space-y-2 p-3'>
                <div className='font-medium'>{period}</div>
                <div>{labels[bucket.status]}</div>
                {bucket.count > 0 && (
                  <div className='flex gap-4 text-xs'>
                    <span>
                      {t('Availability')}:{' '}
                      {formatNumber(
                        (bucket.success_count / bucket.count) * 100,
                        locale
                      )}
                      %
                    </span>
                    <span>
                      {t('{{count}} samples', { count: bucket.count })}
                    </span>
                  </div>
                )}
                <div className='text-xs'>
                  {t('Average first-token latency')}:{' '}
                  {bucket.ttft_ms == null
                    ? '—'
                    : `${formatNumber(bucket.ttft_ms / 1000, locale)} s`}
                </div>
              </TooltipContent>
            </Tooltip>
          )
        })}
      </div>
      {!props.timeline.some((bucket) => bucket.count > 0) && (
        <p className='text-muted-foreground text-[11px]'>
          {t('Awaiting the first sample')}
        </p>
      )}
    </div>
  )
}
