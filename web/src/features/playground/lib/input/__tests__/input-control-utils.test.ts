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
import { describe, expect, test } from 'vitest'

import {
  getInputControlState,
  getSubmittableInputText,
} from '../input-control-utils'

const image: FileUIPart = {
  type: 'file',
  mediaType: 'image/png',
  filename: 'photo.png',
  url: 'data:image/png;base64,aW1hZ2U=',
}

describe('playground input submission', () => {
  test.each([
    { text: '', files: [], expected: null },
    { text: '  ', files: [], expected: null },
    { text: ' original text ', files: [], expected: ' original text ' },
    { text: '', files: [image], expected: '' },
    { text: undefined, files: [image], expected: '' },
  ])(
    'returns $expected for text "$text" with $files.length attachments',
    (options) => {
      expect(getSubmittableInputText(options)).toBe(options.expected)
    }
  )

  test('rejects a populated draft when the input is disabled', () => {
    expect(
      getSubmittableInputText({ text: 'Describe', files: [image] }, true)
    ).toBeNull()
  })

  test.each([
    { hasAttachments: true, disabled: false, hasModels: true, expected: true },
    {
      hasAttachments: false,
      disabled: false,
      hasModels: true,
      expected: false,
    },
    { hasAttachments: true, disabled: true, hasModels: true, expected: false },
    {
      hasAttachments: true,
      disabled: false,
      hasModels: false,
      expected: false,
    },
  ])(
    'allows image-only submission=$expected when attachments=$hasAttachments disabled=$disabled models=$hasModels',
    (options) => {
      expect(
        getInputControlState({
          disabled: options.disabled,
          groups: [{ label: 'Default', value: 'default', ratio: 1 }],
          hasAttachments: options.hasAttachments,
          hasStopHandler: false,
          models: options.hasModels
            ? [{ label: 'Vision', value: 'vision' }]
            : [],
          text: '',
        }).canSubmit
      ).toBe(options.expected)
    }
  )
})
