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
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'

import { api } from '@/lib/api'
import { requireServerSuccess } from '@/lib/server-error-message'
import { useAuthStore } from '@/stores/auth-store'

import type { RankingPeriod } from '../types'

export type RankedUser = {
  rank: number
  name: string
  nickname?: string
  masked_username?: string
  total_tokens: number
  is_self: boolean
}

export type UserRankingSnapshot = {
  users: RankedUser[]
  participants: number
  total_tokens: number
  period_start: number
  period_end: number
  updated_at: number
  self?: RankedUser
}

export type Participation = {
  participating: boolean
  prompted: boolean
  nickname?: string
  nickname_changed_at?: number
  next_nickname_change_at?: number
}
type Response<T> = { success: boolean; message?: string; data: T }

export function useUserRankings(period: RankingPeriod) {
  const user = useAuthStore((state) => state.auth.user)
  const queryClient = useQueryClient()
  const preferenceKey = ['ranking-participation', user?.id]
  const rankings = useQuery({
    queryKey: ['user-rankings', period, user?.id],
    queryFn: async () => {
      const response = await api.get<Response<UserRankingSnapshot>>(
        '/api/rankings/users',
        { params: { period } }
      )
      return requireServerSuccess(response.data).data
    },
    staleTime: 0,
    refetchInterval: 30_000,
  })
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
    mutationFn: async (participating: boolean) => {
      const response = await api.put<Response<Participation>>(
        '/api/user/self/ranking',
        { participating }
      )
      return requireServerSuccess(response.data).data
    },
    onSuccess: (data) => {
      queryClient.setQueryData(preferenceKey, data)
      // Remove the current user's row immediately while a fresh ranking is fetched.
      if (!data.participating) {
        queryClient.setQueriesData<UserRankingSnapshot>(
          { queryKey: ['user-rankings'] },
          (previous) => {
            if (!previous) return previous
            return {
              ...previous,
              self: undefined,
              users: previous.users
                .filter((entry) => !entry.is_self)
                .map((entry, index) => ({ ...entry, rank: index + 1 })),
              total_tokens: Math.max(
                0,
                previous.total_tokens - (previous.self?.total_tokens ?? 0)
              ),
            }
          }
        )
      }
      void queryClient.invalidateQueries({ queryKey: ['user-rankings'] })
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
