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
import { z } from 'zod'

export const HOMEPAGE_QUERY_KEY = ['homepage-content'] as const
export const HOMEPAGE_MAX_BYTES = 4 * 1024 * 1024
export const HOMEPAGE_MESSAGE = 'new-api.homepage/v1'

const packageBase = {
  schemaVersion: z.literal(1),
  id: z.string().regex(/^[a-z][a-z0-9-]{0,63}$/),
  name: z.string().trim().min(1).max(100),
  description: z.string().max(500).optional(),
  layout: z.enum(['content', 'full']),
  config: z.record(z.string(), z.unknown()).optional(),
  locales: z.record(z.string(), z.record(z.string(), z.string())).optional(),
}

export const homepagePackageSchema = z.discriminatedUnion('kind', [
  z
    .object({
      ...packageBase,
      kind: z.literal('html'),
      html: z.string().trim().min(1),
      css: z.string().optional(),
      javascript: z.string().optional(),
    })
    .strict(),
  z
    .object({
      ...packageBase,
      kind: z.literal('url'),
      url: z.string().refine((value) => {
        try {
          const url = new URL(value)
          return (
            ['https:', 'http:'].includes(url.protocol) &&
            !url.username &&
            !url.password
          )
        } catch {
          return false
        }
      }),
    })
    .strict(),
  z
    .object({
      ...packageBase,
      kind: z.literal('builtin'),
      id: z.enum(['official', 'codeq']),
    })
    .strict(),
])

export type HomepagePackage = z.infer<typeof homepagePackageSchema>
export type HomepageRegistry = {
  schemaVersion: 1
  active: string
  packages: HomepagePackage[]
}
export const BUILTIN_HOMEPAGES: HomepagePackage[] = [
  {
    schemaVersion: 1,
    id: 'official',
    name: 'New API',
    kind: 'builtin',
    layout: 'content',
  },
  {
    schemaVersion: 1,
    id: 'codeq',
    name: 'CodeQ',
    kind: 'builtin',
    layout: 'content',
  },
]

export function parseHomepagePackage(raw: string): HomepagePackage {
  if (new TextEncoder().encode(raw).length > HOMEPAGE_MAX_BYTES) {
    throw new Error('Invalid homepage package configuration')
  }
  const result = homepagePackageSchema.safeParse(JSON.parse(raw))
  if (!result.success) throw new Error('Invalid homepage package configuration')
  if (
    result.data.kind !== 'builtin' &&
    ['official', 'codeq', 'legacy'].includes(result.data.id)
  ) {
    throw new Error('Invalid homepage package configuration')
  }
  return result.data
}

export function parseHomepageRegistry(
  raw: string,
  legacy = ''
): HomepageRegistry {
  if (!raw.trim()) {
    return {
      schemaVersion: 1,
      active: legacy ? 'legacy' : 'official',
      packages: [],
    }
  }
  if (new TextEncoder().encode(raw).length > HOMEPAGE_MAX_BYTES) {
    throw new Error('Invalid homepage package configuration')
  }
  const value = z
    .object({
      schemaVersion: z.literal(1),
      active: z.string(),
      packages: z.array(homepagePackageSchema).max(32),
    })
    .strict()
    .parse(JSON.parse(raw))
  const ids = new Set(['official', 'codeq', 'legacy'])
  for (const item of value.packages) {
    if (item.kind === 'builtin' || ids.has(item.id)) {
      throw new Error('Invalid homepage package configuration')
    }
    ids.add(item.id)
  }
  if (!ids.has(value.active)) {
    throw new Error('Invalid homepage package configuration')
  }
  return value
}

export function readPublicHomepagePackage(
  content: string
): HomepagePackage | null {
  if (!content.trimStart().startsWith('{')) return null
  try {
    const value = JSON.parse(content)
    if (value?.schema !== 'new-api.homepage') return null
    return parseHomepagePackage(JSON.stringify(value.package))
  } catch {
    return null
  }
}

export async function resolveHomepagePackage(
  pack: HomepagePackage
): Promise<HomepagePackage> {
  if (pack.kind !== 'builtin' || pack.id !== 'codeq') return pack
  const { default: template } = await import('./packages/codeq.json')
  return parseHomepagePackage(JSON.stringify(template))
}
