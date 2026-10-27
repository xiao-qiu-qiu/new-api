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
import { useTranslation } from 'react-i18next'

import { ResponseImage } from '@/components/ai-elements/response-renderer-image'

import { getMessageImageAttachments } from '../../lib'
import type { Message } from '../../types'

export function PlaygroundMessageAttachments(props: { message: Message }) {
  const { t } = useTranslation()
  const attachments = getMessageImageAttachments(props.message)

  if (attachments.length === 0) return null

  return (
    <div className='flex max-w-full flex-wrap gap-2 [&_img]:my-0 [&_img]:max-h-64'>
      {attachments.map((attachment) => (
        <ResponseImage
          key={attachment.id ?? attachment.url}
          node={{
            type: 'image',
            src: attachment.url,
            alt: attachment.filename || t('Image'),
            title: attachment.filename ?? null,
            raw: '',
          }}
        />
      ))}
    </div>
  )
}
