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
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'

import {
  PromptInput,
  PromptInputAttachment,
  PromptInputAttachments,
  PromptInputButton,
  PromptInputProvider,
  PromptInputTextarea,
  type PromptInputProps,
  usePromptInputAttachments,
} from '../prompt-input'

function AttachmentPicker() {
  const attachments = usePromptInputAttachments()
  return (
    <PromptInputButton onClick={attachments.openFileDialog}>
      Attach
    </PromptInputButton>
  )
}

function InputFixture(
  props: Partial<PromptInputProps> & { provider?: boolean }
) {
  const { provider, ...inputProps } = props
  const input = (
    <PromptInput
      aria-label='Chat input'
      onSubmit={() => undefined}
      {...inputProps}
    >
      <PromptInputAttachments>
        {(file) => <PromptInputAttachment data={file} />}
      </PromptInputAttachments>
      <PromptInputTextarea aria-label='Message' />
      <AttachmentPicker />
      <PromptInputButton type='submit'>Send</PromptInputButton>
    </PromptInput>
  )
  return provider ? <PromptInputProvider>{input}</PromptInputProvider> : input
}

function upload(...files: File[]) {
  fireEvent.change(screen.getByLabelText('Upload files'), { target: { files } })
}

function image(name = 'first.png') {
  return new File(['image'], name, { type: 'image/png' })
}

function fileTransfer(files: File[]) {
  return { types: ['Files'], files }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((resolvePromise) => {
    resolve = resolvePromise
  })
  return { promise, resolve }
}

beforeEach(() => {
  let nextUrl = 0
  vi.spyOn(URL, 'createObjectURL').mockImplementation(() => `blob:${++nextUrl}`)
  vi.spyOn(URL, 'revokeObjectURL').mockImplementation(() => undefined)
  vi.stubGlobal(
    'fetch',
    vi.fn().mockResolvedValue({
      blob: async () => new Blob(['image'], { type: 'image/png' }),
    })
  )
})

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('PromptInput attachments', () => {
  test.each([false, true])(
    'keeps preview URLs live until removal or unmount with provider %s',
    (provider) => {
      const view = render(<InputFixture provider={provider} />)
      upload(image())
      upload(image('second.png'))

      expect(screen.getByRole('img', { name: 'first.png' })).toHaveAttribute(
        'src',
        'blob:1'
      )
      expect(URL.revokeObjectURL).not.toHaveBeenCalled()

      fireEvent.click(
        screen.getAllByRole('button', { name: 'Remove attachment' })[0]
      )
      expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:1')
      expect(URL.revokeObjectURL).not.toHaveBeenCalledWith('blob:2')
      view.unmount()
      expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:2')
    }
  )

  test('applies accepted types, size and count limits when attachment state uses a provider', () => {
    const onError = vi.fn()
    render(
      <InputFixture
        provider
        accept='image/png'
        maxFiles={1}
        maxFileSize={5}
        onError={onError}
      />
    )
    upload(new File(['text'], 'notes.txt', { type: 'text/plain' }))
    upload(new File(['oversized'], 'large.png', { type: 'image/png' }))
    upload(image(), image('second.png'))

    expect(screen.getByRole('img', { name: 'first.png' })).toBeInTheDocument()
    expect(screen.queryByText('notes.txt')).not.toBeInTheDocument()
    expect(screen.queryByText('large.png')).not.toBeInTheDocument()
    expect(screen.queryByText('second.png')).not.toBeInTheDocument()
    expect(onError.mock.calls.map(([error]) => error.code)).toEqual([
      'accept',
      'max_file_size',
      'max_files',
    ])
  })

  test.each([false, true])(
    'adds a dropped file once when globalDrop is %s',
    (globalDrop) => {
      render(<InputFixture globalDrop={globalDrop} />)
      const form = screen.getByRole('form', { name: 'Chat input' })
      const dataTransfer = fileTransfer([image()])

      expect(fireEvent.dragOver(form, { dataTransfer })).toBe(false)
      fireEvent.drop(form, { dataTransfer })

      expect(screen.getAllByRole('img', { name: 'first.png' })).toHaveLength(1)
    }
  )

  test.each([
    ['image/png,image/jpeg,image/gif,image/webp', 'first.png', 'image/png'],
    ['.PNG, .jpg', 'PHOTO.PNG', ''],
    ['image/*', 'first.png', 'image/png'],
  ])(
    'accepts matching files and rejects other types for %s',
    (accept, filename, mediaType) => {
      const onError = vi.fn()
      render(<InputFixture accept={accept} onError={onError} />)
      upload(new File(['image'], filename, { type: mediaType }))
      upload(new File(['text'], 'notes.txt', { type: 'text/plain' }))

      expect(screen.getByText(filename)).toBeInTheDocument()
      expect(screen.queryByText('notes.txt')).not.toBeInTheDocument()
      expect(onError).toHaveBeenCalledWith(
        expect.objectContaining({ code: 'accept' })
      )
    }
  )

  test.each([false, true])(
    'preserves attachments and ignores picker, paste, drop and submit while disabled with provider %s',
    async (provider) => {
      const onSubmit = vi.fn()
      const view = render(
        <InputFixture provider={provider} onSubmit={onSubmit} />
      )
      upload(image())
      view.rerender(
        <InputFixture
          disabled
          globalDrop
          provider={provider}
          onSubmit={onSubmit}
        />
      )
      const input = screen.getByLabelText('Upload files')
      const click = vi.spyOn(input, 'click')
      const textarea = screen.getByRole('textbox', { name: 'Message' })

      fireEvent.click(screen.getByRole('button', { name: 'Attach' }))
      upload(image('picker.png'))
      fireEvent.paste(textarea, {
        clipboardData: {
          items: [{ kind: 'file', getAsFile: () => image('paste.png') }],
        },
      })
      fireEvent.drop(screen.getByRole('form', { name: 'Chat input' }), {
        dataTransfer: fileTransfer([image('drop.png')]),
      })
      fireEvent.keyDown(textarea, { key: 'Backspace' })
      fireEvent.submit(screen.getByRole('form', { name: 'Chat input' }))
      await act(async () => {})

      expect(input).toBeDisabled()
      expect(click).not.toHaveBeenCalled()
      expect(onSubmit).not.toHaveBeenCalled()
      expect(screen.getAllByRole('img', { name: 'first.png' })).toHaveLength(1)
      expect(
        screen.getByRole('button', { name: 'Remove attachment' })
      ).toBeDisabled()
      expect(screen.queryByText('picker.png')).not.toBeInTheDocument()
      expect(screen.queryByText('paste.png')).not.toBeInTheDocument()
      expect(screen.queryByText('drop.png')).not.toBeInTheDocument()
    }
  )

  test('reports file reading failures and keeps the draft and attachment for a successful retry', async () => {
    const onError = vi.fn()
    const onSubmit = vi.fn()
    vi.mocked(fetch).mockRejectedValueOnce(new Error('read failed'))
    render(<InputFixture onError={onError} onSubmit={onSubmit} />)
    upload(image())
    const textarea = screen.getByRole('textbox', { name: 'Message' })
    fireEvent.change(textarea, { target: { value: 'describe this' } })
    fireEvent.submit(screen.getByRole('form', { name: 'Chat input' }))

    await waitFor(() =>
      expect(onError).toHaveBeenCalledWith(
        expect.objectContaining({ code: 'file_read' })
      )
    )
    expect(textarea).toHaveValue('describe this')
    expect(screen.getByRole('img', { name: 'first.png' })).toBeInTheDocument()
    expect(onSubmit).not.toHaveBeenCalled()

    fireEvent.submit(screen.getByRole('form', { name: 'Chat input' }))
    await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce())
    expect(onSubmit.mock.calls[0][0]).toEqual({
      text: 'describe this',
      files: [
        expect.objectContaining({ url: 'data:image/png;base64,aW1hZ2U=' }),
      ],
    })
    await waitFor(() =>
      expect(screen.queryByText('first.png')).not.toBeInTheDocument()
    )
    expect(textarea).toHaveValue('')
  })

  test.each([false, true])(
    'blocks duplicate submit and preserves edits and new attachments during asynchronous submission with provider %s',
    async (provider) => {
      const reading = deferred<{ blob: () => Promise<Blob> }>()
      const submitting = deferred<void>()
      vi.mocked(fetch).mockReturnValueOnce(reading.promise as Promise<Response>)
      const onSubmit = vi.fn(() => submitting.promise)
      render(<InputFixture provider={provider} onSubmit={onSubmit} />)
      upload(image())
      const textarea = screen.getByRole('textbox', { name: 'Message' })
      const form = screen.getByRole('form', { name: 'Chat input' })
      fireEvent.change(textarea, { target: { value: 'original draft' } })
      fireEvent.submit(form)
      fireEvent.submit(form)
      expect(textarea).toHaveValue('original draft')
      upload(image('second.png'))
      fireEvent.change(textarea, { target: { value: 'next draft' } })

      reading.resolve({
        blob: async () => new Blob(['image'], { type: 'image/png' }),
      })
      await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce())
      expect(screen.getByRole('img', { name: 'first.png' })).toBeInTheDocument()
      fireEvent.submit(form)
      expect(onSubmit).toHaveBeenCalledOnce()
      await act(async () => submitting.resolve())

      await waitFor(() =>
        expect(screen.queryByText('first.png')).not.toBeInTheDocument()
      )
      expect(
        screen.getByRole('img', { name: 'second.png' })
      ).toBeInTheDocument()
      expect(textarea).toHaveValue('next draft')
    }
  )

  test.each(['sync', 'async'])(
    'keeps the draft and attachment when %s submission fails',
    async (mode) => {
      const onSubmit = vi.fn(() => {
        if (mode === 'sync') throw new Error('submit failed')
        return Promise.reject(new Error('submit failed'))
      })
      render(<InputFixture onSubmit={onSubmit} />)
      upload(image())
      const textarea = screen.getByRole('textbox', { name: 'Message' })
      fireEvent.change(textarea, { target: { value: 'retry this' } })
      fireEvent.submit(screen.getByRole('form', { name: 'Chat input' }))

      await waitFor(() => expect(onSubmit).toHaveBeenCalledOnce())
      expect(textarea).toHaveValue('retry this')
      expect(screen.getByRole('img', { name: 'first.png' })).toBeInTheDocument()
      expect(URL.revokeObjectURL).not.toHaveBeenCalled()
    }
  )
})
