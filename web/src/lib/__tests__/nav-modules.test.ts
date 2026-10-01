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

import {
  parseHeaderNavModules as parseSettings,
  serializeHeaderNavModules,
} from '@/features/system-settings/maintenance/config'

import { parseHeaderNavModules } from '../nav-modules'

describe('header module access defaults', () => {
  it.each([undefined, '', '{}', '{broken', '{"rankings":true}'])(
    'requires login for rankings when configuration %s omits an auth preference',
    (value) => {
      expect(parseHeaderNavModules(value).rankings.requireAuth).toBe(true)
      expect(parseSettings(value).rankings.requireAuth).toBe(true)
    }
  )

  it.each([false, 'false', 0])(
    'preserves an administrator explicit rankings auth value %s',
    (value) => {
      const config = JSON.stringify({
        rankings: { enabled: true, requireAuth: value },
      })
      expect(parseHeaderNavModules(config).rankings.requireAuth).toBe(false)
      expect(parseSettings(config).rankings.requireAuth).toBe(false)
    }
  )

  it('makes channel status public by default and preserves configured access on settings round trip', () => {
    expect(parseHeaderNavModules(null).channelStatus).toEqual({
      enabled: true,
      requireAuth: false,
    })
    const raw = JSON.stringify({
      channelStatus: { enabled: false, requireAuth: true },
    })
    expect(
      parseHeaderNavModules(serializeHeaderNavModules(parseSettings(raw)))
        .channelStatus
    ).toEqual({ enabled: false, requireAuth: true })
  })
})
