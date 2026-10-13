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
import { useQueries, useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useMemo } from 'react'

import { requireServerSuccess } from '@/lib/server-error-message'

import { getUserGroups, getUserModels } from '../playground/api'
import type { GroupOption, ModelOption } from '../playground/types'
import { getPricing } from '../pricing/api'
import { buildCanvasChannels, type CanvasChannel } from './model-catalog'

export type CanvasCatalog = {
  groups: GroupOption[]
  channels: CanvasChannel[]
  isLoading: boolean
  error: Error | null
  refetch: () => Promise<void>
}

const emptyGroups: GroupOption[] = []
const emptyModels: ModelOption[] = []
const staleTime = 5 * 60 * 1000

export function useCanvasCatalog(): CanvasCatalog {
  const queryClient = useQueryClient()
  const groupsQuery = useQuery({
    queryKey: ['playground-groups'],
    queryFn: getUserGroups,
    staleTime,
  })
  const pricingQuery = useQuery({
    queryKey: ['pricing'],
    queryFn: async () => requireServerSuccess(await getPricing()),
    staleTime,
  })
  const groups = groupsQuery.data ?? emptyGroups
  // Models only depend on groups, never on pricing or another group's models.
  // Keys and data shapes match usePlaygroundOptions, sharing in-flight requests.
  const modelsQuery = useQueries({
    queries: groups.map((group) => ({
      queryKey: ['playground-models', group.value],
      queryFn: () => getUserModels(group.value),
      staleTime,
    })),
    combine: (results) => ({
      models: results.map((result) => result.data ?? emptyModels),
      isLoading: results.some((result) => result.isLoading),
      error: results.find((result) => result.error)?.error ?? null,
    }),
  })
  const channels = useMemo(
    () =>
      buildCanvasChannels(
        groups,
        new Map(
          groups.map((group, index) => [group.value, modelsQuery.models[index]])
        ),
        pricingQuery.data,
        window.location.origin
      ),
    [groups, modelsQuery.models, pricingQuery.data]
  )

  const refetchGroups = groupsQuery.refetch
  const refetchPricing = pricingQuery.refetch
  const refetch = useCallback(async (): Promise<void> => {
    const pricingPromise = refetchPricing()
    const refreshedGroups = await refetchGroups()
    // Use the refreshed group list so newly available groups are included.
    await Promise.all([
      pricingPromise,
      ...(refreshedGroups.data ?? []).map((group) =>
        queryClient.fetchQuery({
          queryKey: ['playground-models', group.value],
          queryFn: () => getUserModels(group.value),
          staleTime: 0,
        })
      ),
    ])
  }, [queryClient, refetchGroups, refetchPricing])

  return {
    groups,
    channels,
    isLoading:
      groupsQuery.isLoading || pricingQuery.isLoading || modelsQuery.isLoading,
    error: groupsQuery.error ?? pricingQuery.error ?? modelsQuery.error,
    refetch,
  }
}
