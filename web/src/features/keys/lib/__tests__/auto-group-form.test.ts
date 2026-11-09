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
import { describe, expect, test } from 'vitest'

import { supportsOpenAITierPolicy } from '@/lib/openai-service-tier'

import { apiKeySchema, type ApiKey } from '../../types'
import {
  getApiKeyFormDefaultValues,
  getApiKeyFormSchema,
  transformApiKeyToFormDefaults,
  transformFormDataToPayload,
} from '../api-key-form'

const t = ((key: string, options?: Record<string, unknown>) => {
  if (options?.max !== undefined) {
    return key.replace('{{max}}', String(options.max))
  }
  return key
}) as TFunction

const baseApiKey: ApiKey = {
  id: 1,
  name: 'test',
  key: 'sk-test',
  status: 1,
  remain_quota: 0,
  used_quota: 0,
  unlimited_quota: true,
  expired_time: -1,
  created_time: 1,
  accessed_time: 0,
  group: 'auto',
  auto_groups: null,
  cross_group_retry: true,
  model_limits_enabled: false,
  model_limits: '',
  allow_ips: '',
  allow_user_quota_query: false,
}

describe('API key explicit group form mapping', () => {
  test('starts new keys with no selected groups and requires a selection', () => {
    const defaults = getApiKeyFormDefaultValues()
    expect(defaults.auto_groups).toEqual([])
    const result = getApiKeyFormSchema(t).safeParse({
      ...defaults,
      name: 'new',
    })
    expect(result.success).toBe(false)
    if (result.success) return
    expect(result.error.issues).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          path: ['auto_groups'],
          message: 'Select at least one group',
        }),
      ])
    )
  })

  test('keeps legacy responses without auto_groups parseable but requires explicit selection', () => {
    const legacy: Record<string, unknown> = { ...baseApiKey }
    delete legacy.auto_groups
    const parsed = apiKeySchema.parse(legacy)
    expect(parsed.auto_groups).toBe(null)
    const defaults = transformApiKeyToFormDefaults(parsed)
    expect(defaults.auto_groups).toEqual([])
    expect(getApiKeyFormSchema(t).safeParse(defaults).success).toBe(false)
  })

  test.each([null, []])(
    'requires selection for a legacy Auto key with %j groups',
    (auto_groups) => {
      const defaults = transformApiKeyToFormDefaults({
        ...baseApiKey,
        auto_groups,
      })
      expect(defaults.auto_groups).toEqual([])
      expect(getApiKeyFormSchema(t).safeParse(defaults).success).toBe(false)
    }
  )

  test('requires selection for a legacy key without a group', () => {
    const defaults = transformApiKeyToFormDefaults({ ...baseApiKey, group: '' })
    expect(defaults.auto_groups).toEqual([])
    expect(getApiKeyFormSchema(t).safeParse(defaults).success).toBe(false)
  })

  test('preserves an existing single group and submits it without cross-group retry', () => {
    const defaults = transformApiKeyToFormDefaults({
      ...baseApiKey,
      group: 'vip',
    })
    expect(defaults.auto_groups).toEqual(['vip'])
    expect(
      getApiKeyFormSchema(t, 2, ['vip', 'default']).safeParse(defaults).success
    ).toBe(true)
    expect(transformFormDataToPayload(defaults)).toMatchObject({
      group: 'vip',
      auto_groups: [],
      cross_group_retry: false,
    })
  })

  test.each([true, false])(
    'preserves ordered multi-group selections with retry %s',
    (retry) => {
      const defaults = transformApiKeyToFormDefaults({
        ...baseApiKey,
        auto_groups: ['vip', 'default'],
        cross_group_retry: retry,
      })
      expect(defaults.auto_groups).toEqual(['vip', 'default'])
      expect(transformFormDataToPayload(defaults)).toMatchObject({
        group: 'auto',
        auto_groups: ['vip', 'default'],
        cross_group_retry: retry,
      })
    }
  )

  test.each([
    {
      group: 'auto',
      auto_groups: ['revoked', 'vip', 'default'],
      selected: ['revoked', 'vip', 'default'],
    },
    { group: 'revoked', auto_groups: [], selected: ['revoked'] },
  ])(
    'preserves unavailable groups for $group keys and rejects saving until resolved',
    ({ group, auto_groups, selected }) => {
      const defaults = transformApiKeyToFormDefaults({
        ...baseApiKey,
        group,
        auto_groups,
      })
      expect(defaults.auto_groups).toEqual(selected)
      const result = getApiKeyFormSchema(t, 3, ['vip', 'default']).safeParse(
        defaults
      )
      expect(result.success).toBe(false)
      if (result.success) return
      expect(result.error.issues).toEqual(
        expect.arrayContaining([
          expect.objectContaining({
            path: ['auto_groups'],
            message:
              'Some selected groups are unavailable. Choose available groups.',
          }),
        ])
      )
    }
  )

  test('keeps an existing order when the administrator lowers the selection limit', () => {
    const defaults = transformApiKeyToFormDefaults({
      ...baseApiKey,
      auto_groups: ['vip', 'default'],
    })
    expect(defaults.auto_groups).toEqual(['vip', 'default'])
    const result = getApiKeyFormSchema(t, 1).safeParse(defaults)
    expect(result.success).toBe(false)
    if (result.success) return
    expect(result.error.issues).toEqual(
      expect.arrayContaining([
        expect.objectContaining({
          path: ['auto_groups'],
          message: 'Select at most 1 groups',
        }),
      ])
    )
  })

  test.each([
    [20, true],
    [21, false],
  ])('accepts at most 20 groups by default: %s groups', (count, valid) => {
    const result = getApiKeyFormSchema(t).safeParse({
      ...getApiKeyFormDefaultValues(),
      name: 'multi-provider key',
      auto_groups: Array.from(
        { length: Number(count) },
        (_, index) => `provider-${index}`
      ),
    })
    expect(result.success).toBe(valid)
  })

  test.each([
    {
      groups: ['vip', 'vip'],
      available: ['vip'],
      message: 'Groups must not contain duplicates',
    },
    {
      groups: ['auto'],
      available: undefined,
      message: 'Some selected groups are unavailable. Choose available groups.',
    },
    {
      groups: ['vip'],
      available: [],
      message: 'Some selected groups are unavailable. Choose available groups.',
    },
  ])(
    'rejects invalid explicit selection $groups',
    ({ groups, available, message }) => {
      const result = getApiKeyFormSchema(t, 2, available).safeParse({
        ...getApiKeyFormDefaultValues(),
        name: 'invalid selection',
        auto_groups: groups,
      })
      expect(result.success).toBe(false)
      if (result.success) return
      expect(result.error.issues).toEqual(
        expect.arrayContaining([
          expect.objectContaining({ path: ['auto_groups'], message }),
        ])
      )
    }
  )
})

describe('API key user quota query form mapping', () => {
  test('lets a new key query the user quota by default', () => {
    const defaults = getApiKeyFormDefaultValues()
    expect(defaults.allow_user_quota_query).toBe(true)
    expect(transformFormDataToPayload(defaults).allow_user_quota_query).toBe(
      true
    )
  })

  test.each([true, false])(
    'keeps the stored value %s when an existing key is edited and saved',
    (stored) => {
      const defaults = transformApiKeyToFormDefaults({
        ...baseApiKey,
        allow_user_quota_query: stored,
      })

      expect(defaults.allow_user_quota_query).toBe(stored)
      expect(transformFormDataToPayload(defaults).allow_user_quota_query).toBe(
        stored
      )
    }
  )
})

describe('API key GPT speed tier form mapping', () => {
  test('defaults new and legacy keys to following the request', () => {
    expect(
      transformFormDataToPayload(getApiKeyFormDefaultValues())
        .openai_tier_policy
    ).toBe('follow')
    for (const stored of [undefined, null, ''] as const) {
      const legacy = apiKeySchema.parse({
        ...baseApiKey,
        openai_tier_policy: stored,
      })
      expect(transformApiKeyToFormDefaults(legacy).openai_tier_policy).toBe(
        'follow'
      )
    }
  })

  test.each(['priority', 'ultrafast'] as const)(
    'preserves %s when editing and saving a key',
    (policy) => {
      const defaults = transformApiKeyToFormDefaults({
        ...baseApiKey,
        openai_tier_policy: policy,
      })
      expect(transformFormDataToPayload(defaults).openai_tier_policy).toBe(
        policy
      )
    }
  )

  test.each([
    ['gpt-6-astra', true],
    ['openai/GPT-6.1-sol', true],
    ['gpt-image-1', false],
    ['gpt-4o-audio-preview', false],
    ['gpt-4o-mini-tts', false],
    ['gpt-4o-transcribe', false],
    ['deepseek-chat', false],
    ['claude-sonnet', false],
    ['gemini-pro', false],
  ])(
    'shows the tier policy for %s only when it is a GPT text model',
    (model, expected) => {
      expect(supportsOpenAITierPolicy(model)).toBe(expected)
    }
  )
})
