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

import { Tabs, TabsList, TabsTrigger } from '@/components/ui/tabs'

import type { RankingPeriod } from '../types'

export function RankingPeriods(props: {
  period: RankingPeriod
  onChange: (period: RankingPeriod) => void
}) {
  const { t } = useTranslation()
  const periods = [
    { id: 'today', label: t('24 hours') },
    { id: 'week', label: t('Week') },
    { id: 'month', label: t('Month') },
    { id: 'year', label: t('Year') },
  ] as const

  return (
    <Tabs
      value={props.period}
      onValueChange={(value) => props.onChange(value as RankingPeriod)}
    >
      <TabsList
        variant='line'
        aria-label={t('Period')}
        className='h-11 border-b'
      >
        {periods.map((period) => (
          <TabsTrigger key={period.id} value={period.id} className='px-4'>
            {period.label}
          </TabsTrigger>
        ))}
      </TabsList>
    </Tabs>
  )
}
