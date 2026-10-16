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
import { EyeOff } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Button } from '@/components/ui/button'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { useSystemConfigStore } from '@/stores/system-config-store'

import type { RankedUser, RankingMetric } from '../hooks/use-user-rankings'

const MODEL_COLORS = [
  '#94bdb4',
  '#c4acd1',
  '#9bb7d7',
  '#d6b0bd',
  '#abbcda',
  '#b6c9ae',
  '#b4b1d6',
  '#a8cbd0',
]

// Stable across users and boards; small segments retain their true proportion.
function modelUsageColor(name: string): string {
  let hash = 2166136261
  for (const character of name) {
    hash = Math.imul(hash ^ character.charCodeAt(0), 16777619) >>> 0
  }
  return MODEL_COLORS[hash % MODEL_COLORS.length]
}

export function ModelUsageBar(props: {
  entry: RankedUser
  metric: RankingMetric
}) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const currency = useSystemConfigStore((state) => state.config.currency)
  if (!props.entry.usage_public) {
    return (
      <span className='text-muted-foreground flex items-center gap-2 text-xs md:justify-center'>
        <EyeOff className='size-3.5' aria-hidden />
        {t('Model usage hidden')}
      </span>
    )
  }
  const models = (props.entry.models ?? []).filter(
    (model) =>
      (props.metric === 'spending'
        ? (model.total_quota ?? 0)
        : (model.total_tokens ?? 0)) > 0
  )
  const total = models.reduce(
    (sum, model) =>
      sum +
      (props.metric === 'spending'
        ? (model.total_quota ?? 0)
        : (model.total_tokens ?? 0)),
    0
  )
  if (!total) {
    return (
      <span className='text-muted-foreground block text-xs md:text-center'>
        {t('No model usage')}
      </span>
    )
  }

  return (
    <div
      className='mx-auto w-full max-w-lg min-w-0'
      role='group'
      aria-label={t('Model breakdown')}
    >
      <div className='flex h-8 w-full items-center'>
        {models.map((model) => {
          const value =
            props.metric === 'spending'
              ? (model.total_quota ?? 0)
              : (model.total_tokens ?? 0)
          const share = (value / total) * 100
          return (
            <Tooltip key={model.model_name}>
              <TooltipTrigger
                delay={0}
                render={
                  <Button
                    variant='ghost'
                    className='group/segment h-8 min-w-0 shrink-0 cursor-help rounded-none border-0 bg-transparent p-0 hover:bg-transparent focus-visible:z-10 first:[&>span]:rounded-l-full last:[&>span]:rounded-r-full last:[&>span]:border-r-0'
                    style={{
                      width: `${share}%`,
                    }}
                    aria-label={`${model.model_name || t('Unknown model')} · ${formatNumber(share, locale)}%`}
                  >
                    <span
                      aria-hidden
                      className='border-background/60 block h-3.5 w-full border-r transition-[filter] group-hover/segment:brightness-95'
                      style={{
                        backgroundColor: modelUsageColor(model.model_name),
                      }}
                    />
                  </Button>
                }
              />
              <TooltipContent
                sideOffset={4}
                className='bg-popover text-popover-foreground max-w-xs min-w-56 flex-col items-stretch gap-3 rounded-xl border p-3.5 shadow-lg [&>[aria-hidden]]:hidden'
              >
                <div className='flex items-center justify-between gap-5 border-b pb-2.5'>
                  <span className='flex min-w-0 items-center gap-2 font-semibold break-all'>
                    <span
                      aria-hidden
                      className='size-2.5 shrink-0 rounded-full'
                      style={{
                        backgroundColor: modelUsageColor(model.model_name),
                      }}
                    />
                    {model.model_name || t('Unknown model')}
                  </span>
                  <span className='text-muted-foreground tabular-nums'>
                    {formatNumber(share, locale)}%
                  </span>
                </div>
                {model.total_tokens != null && (
                  <div className='flex justify-between gap-6'>
                    <span className='text-muted-foreground'>
                      {t('Total tokens')}
                    </span>
                    <span className='font-medium tabular-nums'>
                      {formatNumber(model.total_tokens, locale)}
                    </span>
                  </div>
                )}
                {model.total_quota != null && (
                  <div className='flex justify-between gap-6'>
                    <span className='text-muted-foreground'>
                      {t('Spending')}
                    </span>
                    <span className='font-medium tabular-nums'>
                      {formatBillingCurrencyFromUSD(
                        model.total_quota / currency.quotaPerUnit,
                        { locale }
                      )}
                    </span>
                  </div>
                )}
              </TooltipContent>
            </Tooltip>
          )
        })}
      </div>
    </div>
  )
}
