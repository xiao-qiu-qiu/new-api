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
import { describe, expect, test, vi } from 'vitest'

import { api } from '@/lib/api'

import type { ChannelTestResponse } from '../../types'
import { handleTestChannel } from '../channel-actions'

describe('channel model test client', () => {
  test('posts an empty JSON object for quick connection tests and completes successfully', async () => {
    const response: ChannelTestResponse = { success: true, time: 0.25 }
    const post = vi.spyOn(api, 'post').mockResolvedValue({ data: response })
    const onComplete = vi.fn()

    await handleTestChannel(
      12,
      { channelName: 'Quick test channel', silent: true },
      onComplete
    )

    expect(post).toHaveBeenCalledWith(
      '/api/channel/test/12',
      {},
      expect.any(Object)
    )
    expect(onComplete).toHaveBeenCalledWith(
      true,
      250,
      undefined,
      undefined,
      response
    )
  })

  test('posts custom prompt and selected test options, then forwards the complete reply', async () => {
    const response: ChannelTestResponse = {
      success: true,
      time: 0.125,
      response_text: '这是模型回复',
      response_model: 'upstream-model',
      response_truncated: false,
    }
    const post = vi.spyOn(api, 'post').mockResolvedValue({ data: response })
    const onComplete = vi.fn()

    await handleTestChannel(
      12,
      {
        testModel: 'test-model',
        endpointType: 'responses',
        stream: false,
        prompt: '  请用中文回答\n只说你好  ',
        silent: true,
      },
      onComplete
    )

    expect(post).toHaveBeenCalledWith(
      '/api/channel/test/12',
      {
        model: 'test-model',
        endpoint_type: 'responses',
        stream: false,
        prompt: '  请用中文回答\n只说你好  ',
      },
      expect.objectContaining({
        skipBusinessError: true,
        skipErrorHandler: true,
      })
    )
    expect(onComplete).toHaveBeenCalledWith(
      true,
      125,
      undefined,
      undefined,
      response
    )
  })

  test('posts a prompt without model options and keeps the complete business failure', async () => {
    const response: ChannelTestResponse = {
      success: false,
      message: 'Test failed',
      error_code: 'model_error',
      response_text: 'partial reply',
      response_truncated: true,
      data: { response_time: 42 },
    }
    const post = vi.spyOn(api, 'post').mockResolvedValue({ data: response })
    const onComplete = vi.fn()

    await handleTestChannel(
      12,
      { prompt: 'custom prompt', silent: true },
      onComplete
    )

    expect(post).toHaveBeenCalledWith(
      '/api/channel/test/12',
      { prompt: 'custom prompt' },
      expect.any(Object)
    )
    expect(onComplete).toHaveBeenCalledWith(
      false,
      42,
      'Test failed',
      'model_error',
      response
    )
  })

  test('reports transport failure without a model response', async () => {
    vi.spyOn(api, 'post').mockRejectedValue(new Error('Network error'))
    const onComplete = vi.fn()

    await handleTestChannel(12, { silent: true }, onComplete)

    expect(onComplete).toHaveBeenCalledWith(
      false,
      undefined,
      'Failed to test channel'
    )
  })
})
