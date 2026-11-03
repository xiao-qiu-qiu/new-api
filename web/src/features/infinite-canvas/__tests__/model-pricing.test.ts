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
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, cleanup, renderHook, waitFor } from '@testing-library/react'
import i18next from 'i18next'
import { createElement, type PropsWithChildren } from 'react'
import { afterEach, expect, it } from 'vitest'

import type { PricingData, PricingModel } from '@/features/pricing/types'
import en from '@/i18n/locales/en.json'
import zh from '@/i18n/locales/zh.json'
import {
  DEFAULT_CURRENCY_CONFIG,
  useSystemConfigStore,
} from '@/stores/system-config-store'

import { buildCanvasChannels, type CanvasPricingModel } from '../model-catalog'
import { buildCanvasModelPricing } from '../model-pricing'
import { useCanvasCatalog } from '../use-canvas-catalog'

const group = { value: 'default', label: 'Default', ratio: 2 }
const options = { t: i18next.getFixedT('en'), language: 'en' }

function model(overrides: Partial<PricingModel> = {}): PricingModel {
  return {
    id: 1,
    model_name: 'example',
    quota_type: 0,
    model_ratio: 1,
    completion_ratio: 3,
    enable_groups: ['default'],
    ...overrides,
  }
}

afterEach(async () => {
  cleanup()
  useSystemConfigStore
    .getState()
    .setConfig({ currency: { ...DEFAULT_CURRENCY_CONFIG } })
  await i18next.changeLanguage('en')
  i18next.removeResourceBundle('zhCN', 'translation')
})

it('uses server user-group prices, preserves free prices and carries the server description as text', () => {
  const metadata = model({
    quota_type: 1,
    model_price: 0.05,
    description: '<b>Vendor explanation</b>',
    supported_endpoint_types: ['image-generation'],
  })
  const catalog = { data: [metadata], group_ratio: { default: 0.5 } }
  const channels = buildCanvasChannels(
    [group],
    new Map([['default', [{ value: 'example', label: 'Example' }]]]),
    catalog,
    'https://gateway.example',
    options
  )
  expect(channels[0].models[0]).toMatchObject({
    description: metadata.description,
    pricing: { summary: '$0.025/image' },
  })
  // JSON is the actual cross-frame transport contract, not a cloning utility.
  // eslint-disable-next-line unicorn/prefer-structured-clone
  expect(JSON.parse(JSON.stringify(channels))).toEqual(channels)
  expect(
    buildCanvasModelPricing(
      [metadata],
      group,
      { ...catalog, group_ratio: { default: 0 } },
      options
    ).summary
  ).toBe('$0/image')
  expect(
    buildCanvasModelPricing(
      [{ ...metadata, ignore_group_ratio: true }],
      group,
      catalog,
      options
    ).summary
  ).toBe('$0.05/image')
})

it('displays legacy token prices and image, cache and audio rates with explicit million-token units', () => {
  const price = buildCanvasModelPricing(
    [
      model({
        image_ratio: 2,
        cache_ratio: 0,
        audio_ratio: 3,
        audio_completion_ratio: 2,
      }),
    ],
    group,
    undefined,
    options
  )
  expect(price.summary).toBe('Input $4/1M token · Output $12/1M token')
  expect(price.details).toEqual(
    expect.arrayContaining([
      'Image In $8/1M token',
      'Cache Read $0/1M token',
      'Audio In $12/1M token',
      'Audio Out $24/1M token',
    ])
  )
})

it('keeps fixed request prices and token tiers separate and includes both in the details', () => {
  const price = buildCanvasModelPricing(
    [
      model({
        billing_mode: 'tiered_expr',
        billing_expr:
          'len < 1000 ? tier("request", fixed(0.02)) : tier("tokens", p * 5 + c * 20)',
      }),
    ],
    group,
    undefined,
    options
  )
  expect(price.summary).toContain('$0.04/request')
  expect(price.summary).toContain('Input $10/1M token')
  expect(price.summary).toContain('Token or per-call pricing')
  expect(price.details?.join('\n')).toContain('1,000')
})

it('displays image-count expression prices per image without inventing a request total', () => {
  const price = buildCanvasModelPricing(
    [
      model({
        billing_mode: 'tiered_expr',
        billing_expr: 'tier("image", fixed(0.02)) * image_count',
      }),
    ],
    group,
    undefined,
    options
  )
  expect(price.summary).toBe('Per image $0.04/image')
})

it('uses seconds for legacy Sora/xAI video task prices while preserving HTTP fixed request prices', () => {
  const metadata = model({
    model_name: 'sora-2',
    quota_type: 1,
    model_price: 0.1,
    supported_endpoint_types: ['openai-video'],
    billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
  })
  expect(
    buildCanvasModelPricing([metadata], group, undefined, options).summary
  ).toBe('$0.2/s')
  expect(
    buildCanvasModelPricing(
      [
        {
          ...metadata,
          billing_mode: 'tiered_expr',
          billing_expr: 'tier("request", fixed(0.1))',
          billing_usage_schema: undefined,
        },
      ],
      group,
      undefined,
      options
    ).summary
  ).toBe('Per-call $0.2/request')
})

it('keeps unsupported dynamic expressions out of product price details', () => {
  const price = buildCanvasModelPricing(
    [
      model({
        billing_mode: 'tiered_expr',
        billing_expr: 'unknown_function(1)',
      }),
    ],
    group,
    undefined,
    options
  )
  expect(price).toEqual({
    summary: 'Price unavailable',
    details: ['Price unavailable', 'Special billing expression'],
  })
})

it('shows task seconds, per-image units, constants and localized enum tier conditions', () => {
  const price = buildCanvasModelPricing(
    [
      model({
        billing_mode: 'tiered_expr',
        billing_expr:
          'u("mode") == "pro" ? tier("pro", 0.1 + u("seconds") * 0.8 + u("images") * 0.05) : tier("std", 0.2 + u("seconds") * 0.4 + u("images") * 0.02)',
        billing_usage_schema: {
          mode: {
            enum: ['pro', 'std'],
            enumLabels: {
              pro: { en: 'Professional' },
              std: { en: 'Standard' },
            },
          },
          seconds: {
            type: 'number',
            unit: 'second',
            description: { en: 'Video generation unit price' },
          },
          images: {
            type: 'number',
            unit: 'count',
            unitLabel: { en: 'image' },
            description: { en: 'Image generation unit price' },
          },
        },
      }),
    ],
    group,
    undefined,
    options
  )
  expect(price.summary).toContain('$0.8 – $1.6/s')
  expect(price.summary).toContain('$0.04 – $0.1/image')
  expect(price.summary).toContain('Additional charge')
  expect(price.details?.join('\n')).toContain('Professional')
  expect(price.details?.join('\n')).toContain('$0.2/request')
  expect(price.details?.join('\n')).toContain('$0.4/request')
})

it('keeps provider prices using the same field with different units separate', () => {
  const price = buildCanvasModelPricing(
    [
      model({
        billing_plugin_variants: [
          {
            plugin_key: 'tokens',
            plugin_name: 'Token vendor',
            billing_mode: 'tiered_expr',
            billing_expr: 'tier("base", u("units") * 9.8 / 1000000)',
            billing_usage_schema: { units: { type: 'number', unit: 'token' } },
          },
          {
            plugin_key: 'credits',
            plugin_name: 'Credit vendor',
            billing_mode: 'tiered_expr',
            billing_expr: 'tier("base", u("units") * 0.14)',
            billing_usage_schema: { units: { type: 'number', unit: 'credit' } },
          },
        ],
      }),
    ],
    group,
    undefined,
    options
  )
  expect(price.summary).toContain('$19.6/1M token')
  expect(price.summary).toContain('$0.28/credit')
  expect(price.summary).not.toContain(' – ')
  expect(price.details?.join('\n')).toContain('Token vendor:')
  expect(price.details?.join('\n')).toContain('Credit vendor:')
})

it.each<CanvasPricingModel>([
  { model_name: 'missing' },
  model({ quota_type: 1, model_price: undefined }),
  model({ model_ratio: -1 }),
  model({ billing_mode: 'tiered_expr', billing_expr: '' }),
  model({ billing_mode: 'tiered_expr', billing_expr: 'unknown_function(1)' }),
  model({
    billing_usage_schema: { seconds: { type: 'number', unit: 'second' } },
  }),
])(
  'shows unavailable instead of zero for missing or unsupported pricing (%j)',
  (metadata) => {
    expect(
      buildCanvasModelPricing([metadata], group, undefined, options).summary
    ).toBe('Price unavailable')
  }
)

it('uses only eligible automatic groups and exposes their separate prices and unknown ratios', () => {
  const metadata = model({
    quota_type: 1,
    model_price: 0.1,
    enable_groups: ['vip', 'unknown'],
  })
  const price = buildCanvasModelPricing(
    [metadata],
    { ...group, value: 'auto', ratio: 'auto' },
    {
      data: [metadata],
      auto_groups: ['default', 'vip', 'unknown'],
      group_ratio: { default: 1, vip: 0.5 },
    },
    options
  )
  expect(price.summary).toBe('$0.05/request · Price unavailable')
  expect(price.details).toEqual([
    'vip: $0.05/request',
    'unknown: Price unavailable',
  ])
})

it.each([
  ['en', '$1,234.5'],
  ['zhCN', '$1,234.5'],
  ['zhTW', '$1,234.5'],
  ['fr', '1\u202f234,5\u00a0$'],
  ['ja', '$1,234.5'],
  ['ru', '1\u00a0234,5\u00a0$'],
  ['vi', '1.234,5\u00a0$'],
])(
  'formats fixed and dynamic prices using the %s interface locale',
  (language, amount) => {
    const display = { ...options, language }
    const fixed = model({ quota_type: 1, model_price: 1234.5 })
    const dynamic = model({
      billing_mode: 'tiered_expr',
      billing_expr: 'tier("base", p * 1234.5 + c * 2)',
    })
    expect(
      buildCanvasModelPricing(
        [fixed],
        { ...group, ratio: 1 },
        undefined,
        display
      ).summary
    ).toBe(`${amount}/request`)
    expect(
      buildCanvasModelPricing(
        [dynamic],
        { ...group, ratio: 1 },
        undefined,
        display
      ).summary
    ).toContain(`Input ${amount}/1M token`)
  }
)

it('falls back to runtime formatting for an invalid interface locale', () => {
  const metadata = model({ quota_type: 1, model_price: 1234.5 })
  const fallback = buildCanvasModelPricing([metadata], group, undefined, {
    ...options,
    language: 'invalid_locale!',
  })
  const runtime = buildCanvasModelPricing([metadata], group, undefined, {
    ...options,
    language: '',
  })
  expect(fallback).toEqual(runtime)
})

it('marks conditional multipliers and retains their conditions in the details', () => {
  const price = buildCanvasModelPricing(
    [
      model({
        billing_mode: 'tiered_expr',
        billing_expr:
          'tier("base", p * 2 + c * 5) * (param("quality") == "high" ? 2 : 1)',
      }),
    ],
    group,
    undefined,
    options
  )
  expect(price.summary).toContain('Conditional multipliers')
  expect(price.details?.join('\n')).toContain('×2')
  expect(price.details?.join('\n')).not.toContain('param(')
})

it('uses the current time period for summaries while retaining all period prices in details', () => {
  const metadata = model({
    billing_mode: 'tiered_expr',
    billing_expr:
      'hour("Asia/Shanghai") < 12 ? tier("morning", p * 2 + c * 5) : tier("afternoon", p * 4 + c * 10)',
  })
  const morning = buildCanvasModelPricing([metadata], group, undefined, {
    ...options,
    now: new Date('2026-11-03T02:00:00Z'),
  })
  const afternoon = buildCanvasModelPricing([metadata], group, undefined, {
    ...options,
    now: new Date('2026-11-03T06:00:00Z'),
  })
  expect(morning.summary).toContain('Input $4/1M token')
  expect(afternoon.summary).toContain('Input $8/1M token')
  expect(morning.details?.join('\n')).toContain('Input $8/1M token')
})

it('refreshes serialized catalog prices when language, currency or server pricing changes', async () => {
  i18next.addResourceBundle('en', 'translation', en.translation, true, true)
  i18next.addResourceBundle('zhCN', 'translation', zh.translation, true, true)
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  })
  const metadata = model({
    quota_type: 1,
    model_price: 0.1,
    supported_endpoint_types: ['image-generation'],
  })
  const pricing: PricingData = {
    success: true,
    data: [metadata],
    vendors: [],
    group_ratio: { default: 0.5 },
    usable_group: {},
    supported_endpoint: {},
    auto_groups: [],
  }
  client.setQueryData(['playground-groups'], [group])
  client.setQueryData(
    ['playground-models', 'default'],
    [{ value: 'example', label: 'Example' }]
  )
  client.setQueryData(['pricing'], pricing)
  const wrapper = (props: PropsWithChildren) =>
    createElement(QueryClientProvider, { client }, props.children)
  const { result, unmount } = renderHook(useCanvasCatalog, { wrapper })
  expect(result.current.channels[0].models[0].pricing?.summary).toBe(
    '$0.05/image'
  )
  await act(async () => {
    await i18next.changeLanguage('zhCN')
  })
  expect(result.current.channels[0].models[0].pricing?.summary).toBe('$0.05/张')
  act(() =>
    useSystemConfigStore.getState().setConfig({
      currency: {
        ...DEFAULT_CURRENCY_CONFIG,
        quotaDisplayType: 'CNY',
        usdExchangeRate: 7,
      },
    })
  )
  expect(result.current.channels[0].models[0].pricing?.summary).toBe('¥0.35/张')
  await act(async () => {
    client.setQueryData(['pricing'], {
      ...pricing,
      group_ratio: { default: 0 },
    })
  })
  await waitFor(() =>
    expect(result.current.channels[0].models[0].pricing?.summary).toBe('¥0/张')
  )
  unmount()
  client.clear()
})
