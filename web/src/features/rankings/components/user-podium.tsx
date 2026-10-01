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
import { Medal, Trophy } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { toIntlLocale } from '@/i18n/languages'
import { formatCompactNumber, formatNumber } from '@/lib/format'

import type { RankedUser } from '../hooks/use-user-rankings'
import { RankingUserName } from './ranking-user-name'

export function UserPodium(props: { users: RankedUser[] }) {
  const { t, i18n } = useTranslation()
  const locale = toIntlLocale(i18n.resolvedLanguage || i18n.language)
  const winners = props.users.filter(
    (entry) => entry.rank >= 1 && entry.rank <= 3
  )

  if (winners.length === 0) return null

  const places = [t('First place'), t('Second place'), t('Third place')]

  return (
    <ol aria-label={t('Top three')} className='user-podium'>
      {winners.map((entry) => (
        <li
          key={entry.rank}
          value={entry.rank}
          data-rank={entry.rank}
          className='user-podium-place'
        >
          <div className='user-podium-person'>
            <div className='user-podium-medal' aria-hidden>
              {entry.rank === 1 ? (
                <Trophy className='size-6 sm:size-7' />
              ) : (
                <Medal className='size-5 sm:size-6' />
              )}
            </div>
            <div className='flex w-full min-w-0 items-center justify-center gap-1.5'>
              <RankingUserName
                entry={entry}
                className='text-sm font-semibold sm:text-base'
              />
              {entry.is_self && (
                <Badge
                  variant='secondary'
                  className='shrink-0 px-1.5 text-[10px]'
                >
                  {t('You')}
                </Badge>
              )}
            </div>
            <Tooltip>
              <TooltipTrigger
                render={
                  <span
                    className='cursor-help text-base font-semibold tabular-nums sm:text-xl'
                    tabIndex={0}
                  />
                }
              >
                {formatCompactNumber(entry.total_tokens, locale)}
                <span className='text-muted-foreground ml-1 text-[10px] font-normal sm:text-xs'>
                  Tokens
                </span>
              </TooltipTrigger>
              <TooltipContent>
                {t('Total tokens')}: {formatNumber(entry.total_tokens, locale)}
              </TooltipContent>
            </Tooltip>
          </div>
          <div className='user-podium-step'>
            <span className='text-[10px] font-medium tracking-wide sm:text-xs'>
              {places[entry.rank - 1]}
            </span>
            <span
              aria-hidden
              className='text-3xl leading-none font-semibold tabular-nums sm:text-4xl'
            >
              {entry.rank}
            </span>
          </div>
        </li>
      ))}
    </ol>
  )
}
