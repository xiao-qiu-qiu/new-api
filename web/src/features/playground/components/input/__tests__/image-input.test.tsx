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
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import type { ComponentProps } from 'react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import { DEFAULT_CONFIG, DEFAULT_PARAMETER_ENABLED } from '../../../constants'
import { PlaygroundInput } from '../playground-input'

const image = new File(['image bytes'], 'photo.png', { type: 'image/png' })
const objectUrls = new Map<string, Blob>()
const revokeObjectURL = vi.fn()

function renderInput(
  options: Partial<ComponentProps<typeof PlaygroundInput>> = {}
) {
  const onSubmit = vi.fn()
  const props: ComponentProps<typeof PlaygroundInput> = {
    config: DEFAULT_CONFIG,
    groups: [{ label: 'Default', value: 'default', ratio: 1 }],
    groupValue: 'default',
    models: [{ label: 'Vision', value: 'vision' }],
    modelValue: 'vision',
    onConfigChange: () => undefined,
    onGroupChange: () => undefined,
    onModelChange: () => undefined,
    onParameterEnabledChange: () => undefined,
    onSubmit,
    parameterEnabled: DEFAULT_PARAMETER_ENABLED,
    ...options,
  }
  const view = render(<PlaygroundInput {...props} />)
  return { ...view, onSubmit, props }
}

beforeEach(() => {
  objectUrls.clear()
  vi.stubGlobal(
    'URL',
    Object.assign(class extends URL {}, {
      createObjectURL: (blob: Blob) => {
        const url = `blob:playground-${objectUrls.size}`
        objectUrls.set(url, blob)
        return url
      },
      revokeObjectURL,
    })
  )
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string) => ({
      blob: async () => objectUrls.get(url),
    }))
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('playground image input', () => {
  test('previews a selected image and removes it from the draft', async () => {
    const user = userEvent.setup()
    renderInput()

    await user.upload(screen.getByLabelText('Upload files'), image)

    expect(screen.getByRole('img', { name: 'photo.png' })).toHaveAttribute(
      'src',
      'blob:playground-0'
    )
    await user.click(screen.getByRole('button', { name: 'Remove attachment' }))

    expect(
      screen.queryByRole('img', { name: 'photo.png' })
    ).not.toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /Send/ })[0]).toBeDisabled()
    expect(revokeObjectURL).toHaveBeenCalledWith('blob:playground-0')
  })

  test('sends an image without text as a data URL and clears the draft', async () => {
    const user = userEvent.setup()
    const { onSubmit } = renderInput()

    await user.upload(screen.getByLabelText('Upload files'), image)
    await user.click(screen.getAllByRole('button', { name: /Send/ })[0])

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith('', [
        expect.objectContaining({
          type: 'file',
          filename: 'photo.png',
          mediaType: 'image/png',
          url: 'data:image/png;base64,aW1hZ2UgYnl0ZXM=',
        }),
      ])
    )
    expect(
      screen.queryByRole('img', { name: 'photo.png' })
    ).not.toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /Send/ })[0]).toBeDisabled()
  })

  test('preserves text typed while the submitted image is being converted', async () => {
    const user = userEvent.setup()
    let resolveImage: (image: Blob) => void = () => undefined
    const pendingImage = new Promise<Blob>((resolve) => {
      resolveImage = resolve
    })
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ blob: () => pendingImage }))
    )
    const { onSubmit } = renderInput()
    const textbox = screen.getByRole('textbox')

    await user.upload(screen.getByLabelText('Upload files'), image)
    await user.type(textbox, 'Describe')
    await user.click(screen.getAllByRole('button', { name: /Send/ })[0])
    await user.type(textbox, ' this image')
    resolveImage(image)

    await waitFor(() =>
      expect(onSubmit).toHaveBeenCalledWith('Describe', [
        expect.objectContaining({ filename: 'photo.png' }),
      ])
    )
    expect(textbox).toHaveValue('Describe this image')
  })

  test('adds a pasted image to the draft', () => {
    renderInput()

    fireEvent.paste(screen.getByRole('textbox'), {
      clipboardData: {
        items: [{ kind: 'file', getAsFile: () => image }],
      },
    })

    expect(screen.getByRole('img', { name: 'photo.png' })).toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /Send/ })[0]).toBeEnabled()
  })

  test('rejects unsupported and oversized image files', async () => {
    const user = userEvent.setup({ applyAccept: false })
    renderInput()
    const upload = screen.getByLabelText('Upload files')

    await user.upload(
      upload,
      new File(['text'], 'notes.txt', { type: 'text/plain' })
    )
    const oversized = new File(['large image'], 'large.png', {
      type: 'image/png',
    })
    Object.defineProperty(oversized, 'size', { value: 5 * 1024 * 1024 + 1 })
    await user.upload(upload, oversized)

    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(screen.getAllByRole('button', { name: /Send/ })[0]).toBeDisabled()
  })

  test('keeps at most four selected images', async () => {
    const user = userEvent.setup()
    renderInput()
    const images = Array.from(
      { length: 5 },
      (_, index) =>
        new File(['image bytes'], `photo-${index}.png`, { type: 'image/png' })
    )

    await user.upload(screen.getByLabelText('Upload files'), images)

    expect(screen.getAllByRole('img')).toHaveLength(4)
    expect(
      screen.queryByRole('img', { name: 'photo-4.png' })
    ).not.toBeInTheDocument()
  })

  test('blocks image additions and submission while the input is disabled', () => {
    const { onSubmit } = renderInput({ disabled: true })
    const textbox = screen.getByRole('textbox')

    fireEvent.paste(textbox, {
      clipboardData: {
        items: [{ kind: 'file', getAsFile: () => image }],
      },
    })
    fireEvent.submit(textbox.closest('form') as HTMLFormElement)

    expect(screen.getByLabelText('Upload files')).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Attach' })).toBeDisabled()
    expect(textbox).toBeDisabled()
    expect(screen.queryByRole('img')).not.toBeInTheDocument()
    expect(onSubmit).not.toHaveBeenCalled()
  })
})
