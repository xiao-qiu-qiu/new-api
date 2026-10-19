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

export type VideoCapabilities = {
  resolutions?: string[]
  aspect_ratios?: string[]
  durations?: number[]
  max_duration_seconds?: number
  max_reference_images?: number
  max_reference_audio?: number
  max_reference_videos?: number
}

export type VideoCapabilitiesSnapshot = {
  model_name: string
  video_capabilities: VideoCapabilities | null
  version: string
}

const optionalCount = z
  .string()
  .refine(
    (value) => value === '' || (/^\d+$/.test(value) && Number(value) <= 100),
    'Enter a whole number between 0 and 100.'
  )

export const videoCapabilitiesSchema = z
  .object({
    resolutions: z
      .array(z.string())
      .refine(
        (values) =>
          values.every(
            (value) =>
              /^\d+[pP]?$/.test(value) &&
              Number(value.replace(/[pP]$/, '')) >= 1 &&
              Number(value.replace(/[pP]$/, '')) <= 16384
          ),
        'Use resolutions such as 480p, 720p or 1080p.'
      )
      .max(64, 'Video capability lists support at most 64 options.'),
    aspect_ratios: z
      .array(z.string())
      .refine(
        (values) => values.every((value) => value.length <= 64),
        'Video capability options must be unique, non-empty strings of at most 64 characters.'
      )
      .refine(
        (values) =>
          values.every((value) => /^(auto|[1-9]\d*:[1-9]\d*)$/.test(value)),
        'Use aspect ratios such as 16:9 or 9:16.'
      )
      .max(64, 'Video capability lists support at most 64 options.'),
    durations: z
      .array(z.string())
      .refine(
        (values) =>
          values.every(
            (value) =>
              /^\d+$/.test(value) && Number(value) > 0 && Number(value) <= 3600
          ),
        'Enter whole seconds between 1 and 3600.'
      )
      .max(64, 'Video capability lists support at most 64 options.'),
    max_duration_seconds: z
      .string()
      .refine(
        (value) =>
          value === '' ||
          (/^\d+$/.test(value) && Number(value) > 0 && Number(value) <= 3600),
        'Enter whole seconds between 1 and 3600.'
      ),
    max_reference_images: optionalCount,
    max_reference_audio: optionalCount,
    max_reference_videos: optionalCount,
  })
  .superRefine((values, ctx) => {
    if (
      values.max_duration_seconds &&
      values.durations.some(
        (value) => Number(value) > Number(values.max_duration_seconds)
      )
    ) {
      ctx.addIssue({
        code: 'custom',
        path: ['durations'],
        message: 'Allowed durations must not exceed the maximum duration.',
      })
    }
  })

export type VideoCapabilitiesDraft = z.infer<typeof videoCapabilitiesSchema>

export function videoCapabilitiesDraft(
  value: VideoCapabilities | null
): VideoCapabilitiesDraft {
  return {
    resolutions: value?.resolutions ?? [],
    aspect_ratios: value?.aspect_ratios ?? [],
    durations: value?.durations?.map(String) ?? [],
    max_duration_seconds: value?.max_duration_seconds?.toString() ?? '',
    max_reference_images: value?.max_reference_images?.toString() ?? '',
    max_reference_audio: value?.max_reference_audio?.toString() ?? '',
    max_reference_videos: value?.max_reference_videos?.toString() ?? '',
  }
}

export function videoCapabilitiesPayload(
  draft: VideoCapabilitiesDraft
): VideoCapabilities | null {
  const result: VideoCapabilities = {}
  if (draft.resolutions.length) {
    result.resolutions = [
      ...new Set(
        draft.resolutions.map(
          (value) => `${Number(value.replace(/[pP]$/, ''))}p`
        )
      ),
    ]
  }
  if (draft.aspect_ratios.length) result.aspect_ratios = draft.aspect_ratios
  if (draft.durations.length) {
    result.durations = [...new Set(draft.durations.map(Number))].sort(
      (a, b) => a - b
    )
  }
  for (const key of [
    'max_duration_seconds',
    'max_reference_images',
    'max_reference_audio',
    'max_reference_videos',
  ] as const) {
    if (draft[key] !== '') result[key] = Number(draft[key])
  }
  return Object.keys(result).length ? result : null
}
