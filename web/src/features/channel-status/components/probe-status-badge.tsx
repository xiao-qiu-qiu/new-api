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

import { StatusBadge, type StatusVariant } from '@/components/status-badge'
import { cn } from '@/lib/utils'

import { statusTextColors } from '../lib/status'
import type { ProbeStatus } from '../types'

export function ProbeStatusBadge(props: {
  status: ProbeStatus
  showDot?: boolean
}) {
  const { t } = useTranslation()
  const labels = {
    green: t('Responsive'),
    yellow: t('Elevated latency'),
    red: t('Slow or unavailable'),
    timeout: t('Timed out'),
    unknown: t('Unknown'),
  }
  const variants: Record<ProbeStatus, StatusVariant> = {
    green: 'success',
    yellow: 'warning',
    red: 'danger',
    timeout: 'danger',
    unknown: 'neutral',
  }
  return (
    <StatusBadge
      variant={variants[props.status]}
      label={labels[props.status]}
      copyable={false}
      showDot={props.showDot}
      type={props.showDot ? 'text' : 'badge'}
      className={cn(
        'text-xs',
        props.showDot && [
          'gap-1.5 [&>span[aria-hidden=true]]:bg-current',
          statusTextColors[props.status],
        ],
        props.status === 'timeout' && 'text-red-900 dark:text-red-300'
      )}
    />
  )
}
