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
import { describe, expect, it } from 'vitest'

import { defaultProbe, probeSchema } from '../lib/schema'

const probe = {
  ...defaultProbe,
  channel_id: 7,
  model: 'gpt-test',
  models: ['gpt-test'],
}

describe('channel probe settings', () => {
  it('accepts multiple group models without requiring an upstream channel', () => {
    expect(
      probeSchema.safeParse({
        ...probe,
        scope: 'group',
        group_name: 'default',
        channel_id: 0,
        models: ['gpt-a', 'gpt-b'],
      }).success
    ).toBe(true)
    expect(
      probeSchema.safeParse({
        ...probe,
        scope: 'group',
        group_name: '',
        channel_id: 0,
      }).success
    ).toBe(false)
    expect(probeSchema.safeParse({ ...probe, models: [] }).success).toBe(false)
  })
  it('accepts a private probe without a public name and strips response-only fields', () => {
    const parsed = probeSchema.parse({ ...probe, id: 10, history: [] })
    expect(parsed).toEqual(probe)
  })

  it('rejects publication without an explicit display name', () => {
    const parsed = probeSchema.safeParse({
      ...probe,
      public: true,
      public_name: '  ',
    })
    expect(parsed.success).toBe(false)
    if (!parsed.success) {
      expect(parsed.error.issues[0].path).toEqual(['public_name'])
    }
  })

  it.each([
    [{ green_ms: 3001, yellow_ms: 3000 }, 'yellow_ms'],
    [{ timeout_seconds: 60, interval_seconds: 30 }, 'timeout_seconds'],
    [{ active_enabled: false, passive_enabled: false }, 'active_enabled'],
  ])(
    'rejects incompatible settings %j on the relevant field',
    (changes, field) => {
      const parsed = probeSchema.safeParse({ ...probe, ...changes })
      expect(parsed.success).toBe(false)
      if (!parsed.success) {
        expect(
          parsed.error.issues.some((issue) => issue.path[0] === field)
        ).toBe(true)
      }
    }
  )

  it('accepts equal thresholds and a paused probe with both sources off', () => {
    expect(
      probeSchema.safeParse({
        ...probe,
        enabled: false,
        active_enabled: false,
        passive_enabled: false,
        green_ms: 1000,
        yellow_ms: 1000,
      }).success
    ).toBe(true)
  })
})
