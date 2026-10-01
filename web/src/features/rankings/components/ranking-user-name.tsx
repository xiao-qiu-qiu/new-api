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
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from '@/components/ui/tooltip'
import { cn } from '@/lib/utils'

import type { RankedUser } from '../hooks/use-user-rankings'

export function RankingUserName(props: {
  entry: RankedUser
  className?: string
}) {
  const className = cn('min-w-0 truncate', props.className)
  if (!props.entry.nickname) {
    return <span className={className}>{props.entry.name}</span>
  }

  return (
    <Tooltip>
      <TooltipTrigger
        render={<span tabIndex={0} className={cn(className, 'cursor-help')} />}
      >
        {props.entry.nickname}
      </TooltipTrigger>
      <TooltipContent>{props.entry.masked_username}</TooltipContent>
    </Tooltip>
  )
}
