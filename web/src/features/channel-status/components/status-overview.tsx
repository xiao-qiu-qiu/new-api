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
import {
  Activity,
  CheckCircle2,
  CircleAlert,
  Clock3,
  Radio,
} from 'lucide-react'
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

import { getProbeStatus } from '../lib/status'
import type { PublicChannelStatus, StatusHours } from '../types'

export function StatusOverview(props: {
  probes: PublicChannelStatus[]
  hours: StatusHours
  updatedAt: number
  fetching: boolean
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const samples = props.probes.reduce(
    (sum, probe) => sum + probe.sample_count,
    0
  )
  const successes = props.probes.reduce(
    (sum, probe) => sum + probe.success_count,
    0
  )
  const attention = props.probes.filter((probe) =>
    ['red', 'timeout'].includes(getProbeStatus(probe))
  ).length
  const available = props.probes.filter(
    (probe) => !probe.stale && probe.latest?.success
  ).length
  const known = props.probes.filter(
    (probe) => getProbeStatus(probe) !== 'unknown'
  ).length
  const time = new Intl.DateTimeFormat(locale, {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
    hourCycle: 'h23',
  })
  let headline = t('Waiting for measurements')
  if (known > 0) {
    headline = attention
      ? t('{{count}} models need attention', { count: attention })
      : t('All models operational')
  }
  const HeadlineIcon = attention ? CircleAlert : CheckCircle2

  return (
    <div className='grid gap-4 md:grid-cols-[1.3fr_1fr]'>
      <section className='bg-card/75 relative overflow-hidden rounded-2xl border px-4 py-3 shadow-xs backdrop-blur-sm sm:px-5'>
        <div className='text-muted-foreground flex items-center gap-2 text-sm'>
          <Activity className='size-4 text-teal-600 dark:text-teal-400' />
          {t('Overall availability')}
        </div>
        <div className='mt-2.5 flex items-end justify-between gap-4'>
          <Tooltip>
            <TooltipTrigger
              render={
                <div
                  tabIndex={0}
                  className='focus-visible:ring-ring rounded-sm outline-none focus-visible:ring-2'
                />
              }
            >
              <p className='text-4xl leading-none font-semibold tracking-tight text-teal-700 tabular-nums dark:text-teal-300'>
                {samples
                  ? formatNumber((successes / samples) * 100, locale)
                  : '—'}
                {samples > 0 && <span className='ml-1 text-2xl'>%</span>}
              </p>
            </TooltipTrigger>
            <TooltipContent>
              {t(
                'Availability uses successful samples in the selected window. Periods without data stay gray.'
              )}
            </TooltipContent>
          </Tooltip>
          <div className='shrink-0 text-right'>
            <div className='text-lg font-semibold tabular-nums'>
              {formatNumber(available, locale)}
              <span className='text-muted-foreground text-sm font-normal'>
                {' '}
                / {formatNumber(props.probes.length, locale)}
              </span>
            </div>
            <div className='text-muted-foreground mt-1 text-xs'>
              {t('Models available')}
            </div>
          </div>
        </div>
        <div className='text-muted-foreground mt-2.5 flex items-center gap-2 text-xs'>
          <Clock3 className='size-3.5' />
          {t('Last {{hours}} hours', { hours: props.hours })}
          <span className='opacity-40'>·</span>
          {t('{{count}} samples', { count: samples })}
        </div>
      </section>
      <section className='bg-card/75 flex flex-col justify-between gap-2 rounded-2xl border px-4 py-3 shadow-xs backdrop-blur-sm sm:px-5'>
        <div>
          <div className='text-muted-foreground flex items-center gap-2 text-sm'>
            <Radio className='size-4' />
            {t('Live model health')}
          </div>
          <div
            className={cn(
              'mt-2.5 flex items-center gap-2 text-lg font-semibold tracking-tight',
              attention
                ? 'text-red-600 dark:text-red-400'
                : 'text-teal-700 dark:text-teal-300'
            )}
          >
            <HeadlineIcon className='size-5 shrink-0' />
            {headline}
          </div>
          <p className='text-muted-foreground mt-1 text-xs'>
            {t('Group routing and upstream responses at a glance.')}
          </p>
        </div>
        <div className='text-muted-foreground flex items-center gap-2 text-xs'>
          <span
            className={cn(
              'size-1.5 rounded-full bg-teal-500',
              props.fetching && 'motion-safe:animate-pulse'
            )}
          />
          {props.updatedAt
            ? t('Updated at {{time}}', { time: time.format(props.updatedAt) })
            : t('Waiting for measurements')}
        </div>
      </section>
    </div>
  )
}
