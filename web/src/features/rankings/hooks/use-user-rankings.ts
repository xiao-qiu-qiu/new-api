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
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
} from '@tanstack/react-query'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import type { RankingPeriod } from '../types'

export type RankingMetric = 'tokens' | 'spending'
export type RankedModel = {
  model_name: string
  total_tokens?: number
  total_quota?: number
}
export type RankedUser = {
  rank: number
  name: string
  nickname?: string
  masked_username?: string
  total_tokens?: number
  total_quota?: number
  usage_public: boolean
  models?: RankedModel[]
  is_self: boolean
}

export type UserRankingSnapshot = {
  users: RankedUser[]
  participants: number
  total_tokens: number
  total_quota: number
  period_start: number
  period_end: number
  updated_at: number
  self?: RankedUser
}

export type Participation = {
  participating: boolean
  spending_participating: boolean
  token_usage_public: boolean
  spending_usage_public: boolean
  prompted: boolean
  nickname?: string
  nickname_changed_at?: number
  next_nickname_change_at?: number
}
type Response<T> = { success: boolean; message?: string; data: T }
export type ParticipationChoice = Pick<
  Participation,
  | 'participating'
  | 'spending_participating'
  | 'token_usage_public'
  | 'spending_usage_public'
>

export function useUserRankings(
  period: RankingPeriod,
  metric: RankingMetric = 'tokens'
) {
  const user = useAuthStore((state) => state.auth.user)
  const queryClient = useQueryClient()
  const preferenceKey = ['ranking-participation', user?.id]
  // Warm both boards together so switching never resets the page to a loading skeleton.
  const boards = useQueries({
    queries: (['tokens', 'spending'] as const).map((boardMetric) => ({
      queryKey: ['user-rankings', period, user?.id, boardMetric],
      queryFn: async () => {
        const response = await api.get<Response<UserRankingSnapshot>>(
          '/api/rankings/users',
          { params: { period, metric: boardMetric } }
        )
        return requireServerSuccess(response.data).data
      },
      staleTime: 0,
      refetchInterval: 30_000,
    })),
  })
  const rankings = boards[metric === 'tokens' ? 0 : 1]
  const preference = useQuery({
    queryKey: preferenceKey,
    queryFn: async () => {
      const response = await api.get<Response<Participation>>(
        '/api/user/self/ranking'
      )
      return requireServerSuccess(response.data).data
    },
    enabled: Boolean(user),
    staleTime: 0,
  })
  const participation = useMutation({
    mutationFn: async (choice: ParticipationChoice) => {
      const response = await api.put<Response<Participation>>(
        '/api/user/self/ranking',
        choice
      )
      return requireServerSuccess(response.data).data
    },
    onSuccess: async (data) => {
      queryClient.setQueryData(preferenceKey, data)
      // Drop every personalized snapshot: a privacy change affects cross-board tooltips too.
      await queryClient.resetQueries({ queryKey: ['user-rankings'] })
    },
  })
  const nickname = useMutation({
    mutationFn: async (value: string) => {
      const response = await api.put<Response<Participation>>(
        '/api/user/self/ranking',
        { nickname: value },
        { skipErrorHandler: true }
      )
      return requireServerSuccess(response.data).data
    },
    onSuccess: (data) => {
      queryClient.setQueryData(preferenceKey, data)
      void queryClient.invalidateQueries({ queryKey: ['user-rankings'] })
    },
    onError: () => {
      void queryClient.invalidateQueries({ queryKey: preferenceKey })
    },
  })
  return { rankings, preference, participation, nickname, user }
}
