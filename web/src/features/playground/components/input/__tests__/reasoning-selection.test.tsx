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
import { afterEach, describe, expect, test, vi } from 'vitest'

import { DEFAULT_CONFIG, DEFAULT_PARAMETER_ENABLED } from '../../../constants'
import { PlaygroundParameterPanel } from '../playground-parameter-panel'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('playground reasoning effort selection', () => {
  test.each([
    ['desktop', 1280],
    ['mobile', 390],
  ] as const)(
    '%s parameters expose reasoning levels and allow returning to Auto',
    async (_viewport, width) => {
      vi.stubGlobal('innerWidth', width)
      const user = userEvent.setup()
      const props = {
        config: DEFAULT_CONFIG,
        onConfigChange: vi.fn(),
        onParameterEnabledChange: vi.fn(),
        parameterEnabled: DEFAULT_PARAMETER_ENABLED,
      }
      const view = render(<PlaygroundParameterPanel {...props} />)

      await user.click(screen.getByRole('button', { name: 'Parameters' }))
      const control = await screen.findByRole('combobox', {
        name: 'Reasoning Effort',
      })

      expect(control).toHaveValue('')
      expect(control).toHaveAccessibleDescription(
        'Auto uses the model default. Available levels depend on the model.'
      )
      await user.selectOptions(control, 'high')
      expect(props.onConfigChange).toHaveBeenLastCalledWith(
        'reasoning_effort',
        'high'
      )

      view.rerender(
        <PlaygroundParameterPanel
          {...props}
          config={{ ...DEFAULT_CONFIG, reasoning_effort: 'high' }}
        />
      )
      expect(control).toHaveValue('high')

      await user.selectOptions(control, 'none')
      expect(props.onConfigChange).toHaveBeenLastCalledWith(
        'reasoning_effort',
        'none'
      )
      await user.selectOptions(control, '')
      expect(props.onConfigChange).toHaveBeenLastCalledWith(
        'reasoning_effort',
        ''
      )
    }
  )

  test('disabled parameters keep the saved effort and prevent changes', async () => {
    const user = userEvent.setup()
    const props = {
      config: { ...DEFAULT_CONFIG, reasoning_effort: 'xhigh' as const },
      onConfigChange: vi.fn(),
      onParameterEnabledChange: vi.fn(),
      parameterEnabled: DEFAULT_PARAMETER_ENABLED,
    }
    const view = render(<PlaygroundParameterPanel {...props} />)
    await user.click(screen.getByRole('button', { name: 'Parameters' }))
    view.rerender(<PlaygroundParameterPanel {...props} disabled />)

    const control = screen.getByRole('combobox', { name: 'Reasoning Effort' })
    expect(control).toBeDisabled()
    expect(control).toHaveValue('xhigh')
    await user.selectOptions(control, 'low')
    expect(props.onConfigChange).not.toHaveBeenCalled()
  })

  test('the parameter panel and reasoning control are reachable by keyboard', async () => {
    const user = userEvent.setup()
    render(
      <PlaygroundParameterPanel
        config={DEFAULT_CONFIG}
        onConfigChange={vi.fn()}
        onParameterEnabledChange={vi.fn()}
        parameterEnabled={DEFAULT_PARAMETER_ENABLED}
      />
    )

    await user.tab()
    expect(screen.getByRole('button', { name: 'Parameters' })).toHaveFocus()
    await user.keyboard('{Enter}')

    expect(
      await screen.findByRole('combobox', { name: 'Reasoning Effort' })
    ).toHaveFocus()
  })
})
