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
import type { TFunction } from 'i18next'

import { toIntlLocale } from '@/i18n/languages'
import { formatBillingCurrencyFromUSD } from '@/lib/currency'
import { formatNumber } from '@/lib/format'

import type { GroupOption } from '../playground/types'
import { BILLING_VARS, type ParsedTaskTier } from '../pricing/lib/billing-expr'
import { formatBillingCondition } from '../pricing/lib/billing-expression/condition-display'
import { compileBillingExpression } from '../pricing/lib/billing-expression/parser'
import {
  getDynamicPriceEntries,
  getDynamicPriceUnitLabelKey,
  getDynamicPricingSummary,
  isUnconfiguredTaskUsageModel,
  type DynamicPriceEntry,
} from '../pricing/lib/dynamic-price'
import { getEffectiveGroupRatio } from '../pricing/lib/model-helpers'
import { withPluginPricing } from '../pricing/lib/plugin-pricing'
import { calculateTokenPrice } from '../pricing/lib/price'
import {
  taskPriceLabel,
  taskTierConditions,
  taskUsageUnitLabel,
} from '../pricing/lib/task-price-display'
import type { PriceType, PricingModel } from '../pricing/types'
import type {
  CanvasModel,
  CanvasPricingMetadata,
  CanvasPricingModel,
} from './model-catalog'

export type CanvasPriceDisplayOptions = {
  t: TFunction
  language: string
  now?: Date
}

const legacyTokenPrices: Array<[PriceType, string]> = [
  ['input', 'Input'],
  ['output', 'Output'],
  ['cache', 'Cache Read'],
  ['create_cache', 'Cache Write'],
  ['image', 'Image In'],
  ['audio_input', 'Audio In'],
  ['audio_output', 'Audio Out'],
]

function formatCanvasPriceUSD(value: number, language: string): string {
  return formatBillingCurrencyFromUSD(value, {
    digitsLarge: 4,
    digitsSmall: 6,
    abbreviate: false,
    locale: toIntlLocale(language),
  })
}

function canvasPriceEntry(
  entry: DynamicPriceEntry,
  options: CanvasPriceDisplayOptions,
  ratio: number
): string {
  const label =
    entry.labelKind === 'schema'
      ? taskPriceLabel(entry.description, entry.field, options.language)
      : options.t(entry.shortLabel)
  const unitKey = getDynamicPriceUnitLabelKey(entry) ?? '1M token'
  const unit = taskUsageUnitLabel(entry, options.language, options.t(unitKey))
  const min = (entry.minValue ?? entry.value) * ratio
  const max = (entry.maxValue ?? entry.value) * ratio
  if (!Number.isFinite(min) || !Number.isFinite(max) || min < 0 || max < 0) {
    return `${label}: ${options.t('Price unavailable')}`
  }
  const amount = formatCanvasPriceUSD(min, options.language)
  const price =
    min === max
      ? amount
      : `${amount} – ${formatCanvasPriceUSD(max, options.language)}`
  return `${label} ${price}/${unit}`
}

/** Public display only: the gateway remains the source of prices and settlement. */
function canvasProviderPrice(
  model: PricingModel,
  ratio: number,
  options: CanvasPriceDisplayOptions,
  fixedUnit: 'image' | 'request'
): NonNullable<CanvasModel['pricing']> {
  const t = options.t
  const unknown = { summary: t('Price unavailable') }
  const priceOptions = {
    tokenUnit: 'M' as const,
    groupRatioMultiplier: ratio,
    now: options.now,
  }
  const dynamic = getDynamicPricingSummary(model, priceOptions)
  if (dynamic) {
    if (dynamic.isSpecialExpression || dynamic.entries.length === 0) {
      return { ...unknown, details: [t('Special billing expression')] }
    }
    const summaryEntries =
      dynamic.isTaskUsage || dynamic.primaryEntries.length === 0
        ? dynamic.entries
        : dynamic.primaryEntries
    const summary = summaryEntries.map((entry) =>
      canvasPriceEntry(entry, options, ratio)
    )
    if (dynamic.isTimePricing) summary.push(t('Current period price'))
    if (dynamic.isMixedBilling) summary.push(t('Token or per-call pricing'))
    if (dynamic.hasRequestRules) summary.push(t('Conditional multipliers'))
    const details: string[] = []
    for (const tier of dynamic.tiers) {
      let condition: string
      if ('unitPrices' in tier) {
        condition = taskTierConditions(
          tier as ParsedTaskTier,
          model.billing_usage_schema,
          options.language,
          t
        )
      } else if (tier.conditionText) {
        condition =
          formatBillingCondition(tier.conditionText, t, options.language) ??
          t('Applicable conditions')
      } else {
        condition = tier.conditions
          .map((item) => {
            const label =
              BILLING_VARS.find((variable) => variable.key === item.var)
                ?.label ?? item.var
            return `${t(label)} ${item.op} ${formatNumber(item.value, toIntlLocale(options.language))}`
          })
          .join(' · ')
      }
      const prices = getDynamicPriceEntries(tier, {
        ...priceOptions,
        usageSchema: model.billing_usage_schema,
      }).map((entry) => canvasPriceEntry(entry, options, ratio))
      details.push(
        `${condition || t(dynamic.tiers.length > 1 ? 'Other cases' : 'All requests')}: ${prices.join(' · ') || t('Unknown')}`
      )
    }
    const compiled = compileBillingExpression(model.billing_expr ?? '')
    if (compiled.status === 'ready') {
      for (const rule of compiled.requestRules) {
        const condition =
          formatBillingCondition(rule.cond, t, options.language) ??
          t('Applicable conditions')
        details.push(
          `${t('Conditional multipliers')}: ${condition} ×${formatNumber(rule.multiplier, toIntlLocale(options.language))}`
        )
      }
    }
    return { summary: summary.join(' · '), details }
  }
  // An empty configured expression or an incompatible task schema is not a
  // legacy zero price. Do not silently expose stale legacy fields instead.
  if (
    model.billing_mode === 'tiered_expr' ||
    isUnconfiguredTaskUsageModel(model)
  ) {
    return unknown
  }
  if (model.quota_type === 1) {
    const price = model.model_price
    if (typeof price !== 'number' || !Number.isFinite(price) || price < 0) {
      return unknown
    }
    // Sora/xAI legacy task prices multiply their seconds billing ratio;
    // usage expressions above already carry their own complete unit prices.
    const unit =
      model.billing_usage_schema?.seconds?.unit === 'second' ? 's' : fixedUnit
    const amount = price * ratio
    if (!Number.isFinite(amount)) return unknown
    return {
      summary: `${formatCanvasPriceUSD(amount, options.language)}/${t(unit)}`,
    }
  }
  if (
    model.quota_type !== 0 ||
    !Number.isFinite(model.model_ratio) ||
    model.model_ratio < 0
  ) {
    return unknown
  }
  const prices = legacyTokenPrices.flatMap(([type, label]) => {
    const value = calculateTokenPrice(model, type, ratio)
    if (!Number.isFinite(value) || value < 0) {
      return type === 'input' || type === 'output'
        ? [`${t(label)}: ${t('Unknown')}`]
        : []
    }
    return [
      `${t(label)} ${formatCanvasPriceUSD(value, options.language)}/${t('1M token')}`,
    ]
  })
  return { summary: prices.slice(0, 2).join(' · '), details: prices }
}

export function buildCanvasModelPricing(
  models: readonly CanvasPricingModel[],
  group: GroupOption,
  catalog: CanvasPricingMetadata | undefined,
  options: CanvasPriceDisplayOptions
): NonNullable<CanvasModel['pricing']> {
  const summaries: string[] = []
  const details: string[] = []
  for (const metadata of models) {
    // Only fields irrelevant to display receive defaults; absent price fields
    // stay absent so they cannot accidentally turn into a free model.
    const model = { id: 0, enable_groups: [], ...metadata } as PricingModel
    const variants = model.billing_plugin_variants?.length
      ? model.billing_plugin_variants.map((variant) => ({
          model: withPluginPricing(model, variant),
          label: variant.plugin_name || variant.plugin_key,
        }))
      : [{ model, label: '' }]
    // Legacy image fixed prices are multiplied by n in PrepareImageBillingForRequest.
    // Expression fixed() prices declare their own request/image unit instead.
    const imageEndpoint = metadata.supported_endpoint_types?.some(
      (endpoint) => {
        if (endpoint === 'image-generation') return true
        const info = catalog?.supported_endpoint?.[endpoint]
        const path = typeof info === 'string' ? info : info?.path
        return path === '/v1/images/generations' || path === '/v1/images/edits'
      }
    )
    const fixedUnit =
      imageEndpoint || metadata.output_modalities?.includes('image')
        ? 'image'
        : 'request'
    const groups =
      group.value === 'auto'
        ? (catalog?.auto_groups ?? []).filter(
            (candidate) =>
              !metadata.enable_groups ||
              metadata.enable_groups.includes('all') ||
              metadata.enable_groups.includes(candidate)
          )
        : [group.value]
    for (const candidate of groups) {
      let ratio = catalog?.group_ratio?.[candidate]
      if (
        ratio === undefined &&
        candidate === group.value &&
        group.value !== 'auto'
      ) {
        ratio =
          typeof group.ratio === 'number'
            ? group.ratio
            : Number(group.ratio.trim() || Number.NaN)
      }
      for (const variant of variants) {
        const effectiveRatio = getEffectiveGroupRatio(
          variant.model,
          ratio ?? Number.NaN
        )
        const price =
          Number.isFinite(effectiveRatio) && effectiveRatio >= 0
            ? canvasProviderPrice(
                variant.model,
                effectiveRatio,
                options,
                fixedUnit
              )
            : { summary: options.t('Price unavailable') }
        summaries.push(price.summary)
        const label = [group.value === 'auto' ? candidate : '', variant.label]
          .filter(Boolean)
          .join(' · ')
        for (const detail of [price.summary, ...(price.details ?? [])]) {
          details.push(label ? `${label}: ${detail}` : detail)
        }
      }
    }
  }
  return {
    summary:
      [...new Set(summaries)].join(' · ') || options.t('Price unavailable'),
    ...(details.length > 0 ? { details: [...new Set(details)] } : {}),
  }
}
