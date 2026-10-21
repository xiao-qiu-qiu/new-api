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
import { afterEach, beforeEach, describe, expect, test } from 'vitest'

import { useChannelTestStore } from '../channel-test-store'

beforeEach(() => {
  useChannelTestStore.setState(useChannelTestStore.getInitialState())
  localStorage.clear()
})

afterEach(() => {
  useChannelTestStore.setState(useChannelTestStore.getInitialState())
  localStorage.clear()
})

describe('channel test prompts', () => {
  test('keeps the original prompt text independently for each channel and model', () => {
    const setPrompt = useChannelTestStore.getState().setPrompt

    setPrompt(12, 'model-a', '  prompt A\n  ')
    setPrompt(12, 'model-b', 'prompt B')
    setPrompt(13, 'model-a', 'prompt C')

    expect(useChannelTestStore.getState().prompts).toEqual({
      12: { 'model-a': '  prompt A\n  ', 'model-b': 'prompt B' },
      13: { 'model-a': 'prompt C' },
    })
  })

  test('removes empty and whitespace prompts so only the cleared model returns to default', () => {
    const setPrompt = useChannelTestStore.getState().setPrompt
    setPrompt(12, 'model-a', 'prompt A')
    setPrompt(12, 'model-b', 'prompt B')
    setPrompt(13, 'model-a', 'prompt C')

    setPrompt(12, 'model-a', ' \n\t ')

    expect(
      useChannelTestStore.getState().prompts['12']?.['model-a'] ?? ''
    ).toBe('')
    expect(useChannelTestStore.getState().prompts['12']?.['model-b']).toBe(
      'prompt B'
    )
    expect(useChannelTestStore.getState().prompts['13']?.['model-a']).toBe(
      'prompt C'
    )

    setPrompt(12, 'model-b', '')

    expect(Object.hasOwn(useChannelTestStore.getState().prompts, '12')).toBe(
      false
    )
  })

  test('persists and restores special model names as isolated prompt entries', async () => {
    useChannelTestStore.getState().setPrompt(12, '__proto__', 'special prompt')
    const saved = localStorage.getItem('channel-test-prompts-v1')
    expect(saved).toBeTypeOf('string')
    useChannelTestStore.setState({ prompts: {} })
    localStorage.setItem('channel-test-prompts-v1', saved ?? '')

    await useChannelTestStore.persist.rehydrate()

    expect(useChannelTestStore.getState().prompts['12']?.['__proto__']).toBe(
      'special prompt'
    )
    expect(useChannelTestStore.getState().prompts['12']?.['constructor']).toBe(
      undefined
    )

    useChannelTestStore.getState().setPrompt(12, '__proto__', '')

    expect(useChannelTestStore.getState().prompts['12']?.['__proto__']).toBe(
      undefined
    )
  })
})
