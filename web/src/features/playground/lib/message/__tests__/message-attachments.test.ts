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
import type { FileUIPart } from 'ai'
import { assert, expect, test } from 'vitest'

import { DEFAULT_CONFIG, DEFAULT_PARAMETER_ENABLED } from '../../../constants'
import { buildChatCompletionPayload } from '../../streaming/payload-builder'
import {
  appendUserMessagePair,
  applyMessageEdit,
  createRegeneratedMessages,
} from '../conversation-message-utils'
import { getMessageEditorState } from '../message-editor-utils'

const image: FileUIPart = {
  type: 'file',
  mediaType: 'image/png',
  filename: 'photo.png',
  url: 'data:image/png;base64,aW1hZ2U=',
}

test('sending an image-only message includes its image part and excludes the assistant placeholder', () => {
  const messages = appendUserMessagePair([], '', [image])
  const payload = buildChatCompletionPayload(
    messages,
    DEFAULT_CONFIG,
    DEFAULT_PARAMETER_ENABLED
  )

  expect(messages[0].attachments).toEqual([
    { ...image, id: expect.any(String) },
  ])
  expect(payload.messages).toEqual([
    {
      role: 'user',
      content: [{ type: 'image_url', image_url: { url: image.url } }],
    },
  ])
})

test('a later text turn preserves earlier image input and keeps text-only content as a string', () => {
  const firstTurn = appendUserMessagePair([], 'Describe this', [image])
  const messages = appendUserMessagePair(firstTurn.slice(0, 1), 'What color?')
  const payload = buildChatCompletionPayload(
    messages,
    DEFAULT_CONFIG,
    DEFAULT_PARAMETER_ENABLED
  )

  expect(payload.messages).toEqual([
    {
      role: 'user',
      content: [
        { type: 'text', text: 'Describe this' },
        { type: 'image_url', image_url: { url: image.url } },
      ],
    },
    { role: 'user', content: 'What color?' },
  ])
})

test('editing image message text to empty and regenerating retains its attachment', () => {
  const messages = appendUserMessagePair([], 'Describe this', [image])
  const editResult = applyMessageEdit(messages, messages[0].key, '', true)
  assert(editResult)
  expect(editResult?.shouldSend).toBe(true)
  expect(editResult?.messages[0].attachments).toEqual(messages[0].attachments)
  expect(getMessageEditorState(messages[0], '', 'Describe this').canSave).toBe(
    true
  )

  const regenerated = createRegeneratedMessages(
    editResult.messages,
    editResult.messages[1].key
  )
  assert(regenerated)
  const payload = buildChatCompletionPayload(
    regenerated,
    DEFAULT_CONFIG,
    DEFAULT_PARAMETER_ENABLED
  )
  expect(payload.messages[0].content).toEqual([
    { type: 'image_url', image_url: { url: image.url } },
  ])
})
