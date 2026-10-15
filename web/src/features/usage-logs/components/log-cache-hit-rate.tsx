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
import { toIntlLocale } from '@/i18n/languages'
import { formatNumber } from '@/lib/format'
import { cn } from '@/lib/utils'

export function LogCacheHitRate(props: { rate: number | null }) {
  const { t, i18n } = useTranslation()
  if (props.rate === null) return null

  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  let variant: StatusVariant = 'danger'
  if (props.rate > 85) {
    variant = 'success'
  } else if (props.rate >= 75) {
    variant = 'warning'
  }

  return (
    <StatusBadge
      type='text'
      copyable={false}
      variant={variant}
      label={`(${formatNumber(props.rate, locale)}%)`}
      title={t('Cache read tokens / total input tokens')}
      className={cn(
        'align-baseline text-[11px] tabular-nums',
        props.rate < 30 && 'text-red-900 dark:text-red-300'
      )}
    />
  )
}
