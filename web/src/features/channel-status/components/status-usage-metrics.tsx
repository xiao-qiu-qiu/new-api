import type { ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { getCacheHitRateColor } from '@/lib/cache-hit-rate'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useSystemConfigStore } from '@/stores/system-config-store'

import type { PublicChannelStatus } from '../types'
import { StatusPriceDialog } from './status-price-dialog'

function UsageMetric(props: {
  label: string
  value: ReactNode
  hint?: string
  className?: string
}) {
  const label = <dt className='text-muted-foreground'>{props.label}</dt>
  const value = (
    <dd className={cn('font-semibold tabular-nums', props.className)}>
      {props.value}
    </dd>
  )
  const className = 'flex flex-wrap items-baseline gap-x-1.5'

  if (!props.hint) {
    return (
      <div className={className}>
        {label}
        {value}
      </div>
    )
  }
  return (
    <Tooltip>
      <TooltipTrigger
        render={
          <div
            tabIndex={0}
            className={cn(
              className,
              'focus-visible:ring-ring rounded-sm outline-none focus-visible:ring-2'
            )}
          />
        }
      >
        {label}
        {value}
      </TooltipTrigger>
      <TooltipContent>{props.hint}</TooltipContent>
    </Tooltip>
  )
}

export function StatusUsageMetrics(props: { probe: PublicChannelStatus }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  useSystemConfigStore((state) => state.config.currency)
  const probe = props.probe
  const rate = probe.average_cache_hit_rate
  const historicalPrice = probe.estimated_price_per_100m_tokens
  const fixedPrice = probe.configured_price
  const showRate =
    rate != null && Number.isFinite(rate) && rate >= 0 && rate <= 100
  const showCache = showRate || (probe.cache_eligible_count ?? 0) > 0
  const showFixed =
    probe.configured_price_unit != null &&
    fixedPrice != null &&
    Number.isFinite(fixedPrice) &&
    fixedPrice >= 0
  const showHistorical =
    !showFixed &&
    historicalPrice != null &&
    Number.isFinite(historicalPrice) &&
    historicalPrice >= 0
  const estimateProbeId = probe.can_estimate_price ? probe.probe_id : undefined

  if (!showCache && !showFixed && !showHistorical && estimateProbeId == null) {
    return null
  }

  return (
    <div className='mt-2 flex flex-wrap items-center gap-x-4 gap-y-1.5'>
      {(showCache || showFixed || showHistorical) && (
        <dl className='flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs'>
          {showCache && (
            <UsageMetric
              label={t('Average cache hit rate')}
              value={showRate ? `${formatNumber(rate, locale)}%` : '—'}
              className={
                showRate ? getCacheHitRateColor(rate) : 'text-muted-foreground'
              }
              hint={t(
                'Cache data available for {{reported}} of {{total}} sampled requests. Rate is weighted by input tokens.',
                {
                  reported: formatNumber(probe.cache_sample_count, locale),
                  total: formatNumber(
                    probe.cache_eligible_count ?? probe.cache_sample_count,
                    locale
                  ),
                }
              )}
            />
          )}
          {showFixed && (
            <UsageMetric
              label={
                probe.configured_price_unit === 'image'
                  ? t('Price per image')
                  : t('Price per request')
              }
              value={formatBillingCurrencyFromUSD(fixedPrice, {
                locale,
                abbreviate: false,
              })}
            />
          )}
          {showHistorical && (
            <UsageMetric
              label={t('Historical average / 100M tokens')}
              value={`≈${formatBillingCurrencyFromUSD(historicalPrice, { locale, abbreviate: false })}`}
              hint={t(
                'Based on {{samples}} recent token-priced requests, adjusted to your group rate. Context length, cache and service tiers affect the average.',
                {
                  samples: formatNumber(probe.price_sample_count, locale),
                }
              )}
            />
          )}
        </dl>
      )}
      {estimateProbeId != null && (
        <StatusPriceDialog probeId={estimateProbeId} model={probe.model} />
      )}
    </div>
  )
}
