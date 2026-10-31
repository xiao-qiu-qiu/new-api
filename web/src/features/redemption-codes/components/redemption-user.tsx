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

import { TruncatedCell } from '@/components/data-table'
import { formatTimestampToDate } from '@/lib/format'

import type { Redemption } from '../types'

export function RedemptionUser(props: { redemption: Redemption }) {
  const { t } = useTranslation()
  const redemption = props.redemption

  if (!redemption.used_user_id) {
    return <span className='text-muted-foreground text-sm'>-</span>
  }

  const label = [
    t('User {{id}}', { id: redemption.used_user_id }),
    redemption.used_username,
    redemption.used_user_remark,
  ]
    .filter(Boolean)
    .join(' · ')

  return (
    <TruncatedCell
      className='max-w-[280px] text-sm'
      tabIndex={0}
      tooltipContent={
        <div className='space-y-1 text-xs'>
          <div>
            {t('User ID:')} {redemption.used_user_id}
          </div>
          {redemption.used_username && (
            <div>
              {t('Username')}: {redemption.used_username}
            </div>
          )}
          {redemption.used_user_remark && (
            <div>
              {t('Remark')}: {redemption.used_user_remark}
            </div>
          )}
          {redemption.redeemed_time > 0 && (
            <div>
              {t('Redeemed at')}:{' '}
              {formatTimestampToDate(redemption.redeemed_time)}
            </div>
          )}
        </div>
      }
    >
      {label}
    </TruncatedCell>
  )
}
