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
import { expect, test } from 'vitest'

import { DEFAULT_CONFIG, DEFAULT_PARAMETER_ENABLED } from '../../../constants'
import type { Message, PlaygroundServiceTier } from '../../../types'
import { buildChatCompletionPayload } from '../payload-builder'

const messages: Message[] = [
  {
    key: 'question',
    from: 'user',
    versions: [{ id: 'question-v1', content: 'Hello' }],
  },
]

test.each<{ serviceTier: PlaygroundServiceTier; stream: boolean }>([
  { serviceTier: 'default', stream: true },
  { serviceTier: 'priority', stream: false },
  { serviceTier: 'ultrafast', stream: true },
  { serviceTier: 'flex', stream: false },
])(
  'GPT requests forward $serviceTier with stream=$stream',
  ({ serviceTier, stream }) => {
    const payload = buildChatCompletionPayload(
      messages,
      {
        ...DEFAULT_CONFIG,
        model: 'openai/gpt-5',
        service_tier: serviceTier,
        stream,
      },
      DEFAULT_PARAMETER_ENABLED
    )

    expect(payload).toMatchObject({
      model: 'openai/gpt-5',
      service_tier: serviceTier,
      stream,
      messages: [{ role: 'user', content: 'Hello' }],
    })
  }
)

test.each([
  'claude-sonnet-4-5',
  'deepseek-chat',
  'gemini-2.5-pro',
  'gpt-image-1',
  'gpt-4o-audio-preview',
  'gpt-4o-realtime-preview',
  'gpt-4o-mini-tts',
  'gpt-5-embedding',
  'gpt-4o-transcribe',
])(
  'switching to %s excludes every saved speed tier from its request',
  (model) => {
    const tiers: PlaygroundServiceTier[] = [
      'default',
      'priority',
      'ultrafast',
      'flex',
    ]
    for (const serviceTier of tiers) {
      const payload = buildChatCompletionPayload(
        messages,
        { ...DEFAULT_CONFIG, model, service_tier: serviceTier },
        DEFAULT_PARAMETER_ENABLED
      )

      expect(payload.model).toBe(model)
      expect(payload).not.toHaveProperty('service_tier')
    }
  }
)
