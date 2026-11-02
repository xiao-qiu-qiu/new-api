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
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect } from 'react'

import { isHttpUrl } from '@/lib/content-format'
import { requireServerSuccess } from '@/lib/server-error-message'

import { getHomePageContent } from '../api'
import { HOMEPAGE_QUERY_KEY } from '../packages'
import type { HomePageContentResult } from '../types'

/**
 * Hook to load and manage custom home page content
 * Supports both Markdown/HTML content and iframe URLs
 */
export function useHomePageContent(): HomePageContentResult {
  const queryClient = useQueryClient()
  const query = useQuery({
    queryKey: HOMEPAGE_QUERY_KEY,
    queryFn: async () =>
      requireServerSuccess(await getHomePageContent()).data ?? '',
    staleTime: 0,
    refetchInterval: 30000,
    refetchOnWindowFocus: true,
  })
  useEffect(() => {
    if (typeof BroadcastChannel === 'undefined') return
    const channel = new BroadcastChannel('new-api-homepage')
    channel.addEventListener('message', () => {
      void queryClient.invalidateQueries({ queryKey: HOMEPAGE_QUERY_KEY })
    })
    return () => channel.close()
  }, [queryClient])
  const content = query.data ?? ''
  const isUrl = isHttpUrl(content)
  return { content, isLoaded: !query.isPending, isUrl }
}
