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

interface CacheHitRateUsage {
  cache_tokens?: number
  input_tokens_total?: number
  usage_semantic?: string
  claude?: boolean
  cache_write_tokens?: number
  cache_creation_tokens?: number
  cache_creation_tokens_5m?: number
  cache_creation_tokens_1h?: number
}

/** Rates use percentage points, e.g. 85 means 85%. */
export function getCacheHitRateColor(rate: number): string {
  if (rate > 85) return 'text-emerald-700 dark:text-emerald-400'
  if (rate >= 75) return 'text-amber-700 dark:text-amber-400'
  return 'text-red-700 dark:text-red-400'
}

export function getLogCacheHitRate(
  log: { type: number; prompt_tokens: number },
  other: CacheHitRateUsage | null
): number | null {
  const cacheRead = other?.cache_tokens
  if (
    log.type !== 2 ||
    !other ||
    typeof cacheRead !== 'number' ||
    !Number.isSafeInteger(cacheRead) ||
    cacheRead < 0
  ) {
    return null
  }

  let inputTokens = other.input_tokens_total
  if (inputTokens === undefined) {
    inputTokens = log.prompt_tokens
    if (!Number.isSafeInteger(inputTokens) || inputTokens < 0) return null

    // The upstream format marker alone does not override explicit usage semantics.
    // Expression billing keeps the same raw token semantics as legacy billing.
    const isClaude =
      other.usage_semantic === 'anthropic' ||
      (!other.usage_semantic && other.claude === true)
    if (isClaude) {
      const creationCounts = [
        other.cache_write_tokens,
        other.cache_creation_tokens,
        other.cache_creation_tokens_5m,
        other.cache_creation_tokens_1h,
      ]
      if (
        creationCounts.some(
          (tokens) =>
            tokens !== undefined &&
            (!Number.isSafeInteger(tokens) || tokens < 0)
        )
      ) {
        return null
      }

      const splitCreation =
        (other.cache_creation_tokens_5m ?? 0) +
        (other.cache_creation_tokens_1h ?? 0)
      const cacheWrite =
        other.cache_write_tokens ??
        (splitCreation > 0 ? splitCreation : (other.cache_creation_tokens ?? 0))
      inputTokens += cacheRead + cacheWrite
    }
  }

  if (
    !Number.isSafeInteger(inputTokens) ||
    inputTokens <= 0 ||
    cacheRead > inputTokens
  ) {
    return null
  }
  return (cacheRead / inputTokens) * 100
}
