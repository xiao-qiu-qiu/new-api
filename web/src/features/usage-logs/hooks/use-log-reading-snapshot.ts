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
import { useState } from 'react'

import type { UsageLog } from '../data/schema'
import { getActiveRequest } from '../lib/active-requests'

export function useLogReadingSnapshot(options: {
  logs: UsageLog[]
  scope: string
  enabled: boolean
}) {
  const [snapshot, setSnapshot] = useState<{
    scope: string
    logs: UsageLog[]
    signature: string
  } | null>(null)
  const signature = options.logs
    .map((log) => {
      const active = getActiveRequest(log)
      return active
        ? `active:${active.request_id}:${active.status}:${active.attempt}`
        : `log:${log.request_id}:${log.id}:${log.type}`
    })
    .join('\n')
  const current =
    options.enabled && snapshot?.scope === options.scope ? snapshot : null

  // A permission, account or filter change must never render the old snapshot.
  if (snapshot && (!options.enabled || snapshot.scope !== options.scope)) {
    setSnapshot(null)
  }

  return {
    logs: current?.logs ?? options.logs,
    isPaused: current !== null,
    hasUpdates: current !== null && current.signature !== signature,
    pause: () => {
      if (options.enabled && !current) {
        setSnapshot({ scope: options.scope, logs: options.logs, signature })
      }
    },
    resume: () => setSnapshot(null),
  }
}
