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
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'

import type { Message } from '../../../types'
import { MessageActions } from '../message-actions'
import { PlaygroundMessageContent } from '../playground-message-content'

const imageMessage: Message = {
  key: 'image-message',
  from: 'user',
  versions: [{ id: 'v1', content: '' }],
  attachments: [
    {
      type: 'file',
      filename: 'photo.png',
      mediaType: 'image/png',
      url: 'data:image/png;base64,aW1hZ2U=',
    },
  ],
}

describe('playground image messages', () => {
  test('keeps the image and actions visible when the message has no text', () => {
    render(
      <PlaygroundMessageContent
        actions={
          <MessageActions message={imageMessage} onEdit={() => undefined} />
        }
        alignment='right'
        message={imageMessage}
        versionContent=''
      />
    )

    expect(screen.getByRole('img', { name: 'photo.png' })).toHaveAttribute(
      'src',
      imageMessage.attachments?.[0].url
    )
    expect(screen.getByRole('button', { name: 'Edit' })).toBeEnabled()
  })

  test('offers edit and regenerate actions without copying an image-only message', async () => {
    const user = userEvent.setup()
    const onEdit = vi.fn()
    const onRegenerate = vi.fn()
    render(
      <MessageActions
        message={imageMessage}
        onEdit={onEdit}
        onRegenerate={onRegenerate}
      />
    )

    expect(
      screen.queryByRole('button', { name: 'Copy' })
    ).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Edit' }))
    await user.click(screen.getByRole('button', { name: 'Regenerate' }))

    expect(onEdit).toHaveBeenCalledWith(imageMessage)
    expect(onRegenerate).toHaveBeenCalledWith(imageMessage)
  })
})
