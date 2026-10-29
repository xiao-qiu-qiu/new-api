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

import { DEFAULT_CONFIG, DEFAULT_PARAMETER_ENABLED } from '../../../constants'
import { PlaygroundInput } from '../playground-input'
import { PlaygroundSpeedTierSelect } from '../playground-speed-tier-select'

describe('playground speed tier selection', () => {
  test('selecting UF in the input updates the playground configuration', async () => {
    const user = userEvent.setup()
    const onConfigChange = vi.fn()
    render(
      <PlaygroundInput
        config={{
          ...DEFAULT_CONFIG,
          model: 'gpt-6-sol',
          service_tier: 'default',
        }}
        groups={[{ label: 'Default', value: 'default', ratio: 1 }]}
        groupValue='default'
        models={[{ label: 'GPT', value: 'gpt-6-sol' }]}
        modelValue='gpt-6-sol'
        onConfigChange={onConfigChange}
        onGroupChange={vi.fn()}
        onModelChange={vi.fn()}
        onParameterEnabledChange={vi.fn()}
        onSubmit={vi.fn()}
        parameterEnabled={DEFAULT_PARAMETER_ENABLED}
      />
    )

    await user.click(
      screen.getAllByRole('combobox', { name: 'GPT speed tier' })[0]
    )
    await user.click(
      await screen.findByRole('option', { name: 'Ultrafast tier' })
    )

    expect(onConfigChange).toHaveBeenCalledWith('service_tier', 'ultrafast')
  })

  test.each(['claude-sonnet-4-6', 'gpt-image-1', 'gpt-4o-realtime-preview'])(
    'switching to %s hides the control and switching back restores the selection',
    (model) => {
      const props = { value: 'ultrafast' as const, onChange: vi.fn() }
      const view = render(
        <PlaygroundSpeedTierSelect {...props} model='gpt-6-sol' />
      )
      expect(
        screen.getByRole('combobox', { name: 'GPT speed tier' })
      ).toHaveTextContent('Ultrafast tier')

      view.rerender(<PlaygroundSpeedTierSelect {...props} model={model} />)
      expect(
        screen.queryByRole('combobox', { name: 'GPT speed tier' })
      ).not.toBeInTheDocument()

      view.rerender(
        <PlaygroundSpeedTierSelect {...props} model='openai/gpt-6-sol' />
      )
      expect(
        screen.getByRole('combobox', { name: 'GPT speed tier' })
      ).toHaveTextContent('Ultrafast tier')
      expect(props.onChange).not.toHaveBeenCalled()
    }
  )

  test('keyboard navigation selects the Fast tier', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(
      <PlaygroundSpeedTierSelect
        model='gpt-6-sol'
        value='default'
        onChange={onChange}
      />
    )

    await user.tab()
    expect(
      screen.getByRole('combobox', { name: 'GPT speed tier' })
    ).toHaveFocus()
    await user.keyboard('{ArrowDown}{ArrowDown}{Enter}')

    expect(onChange).toHaveBeenCalledWith('priority')
  })

  test('a disabled speed selector cannot open or change the selected tier', async () => {
    const user = userEvent.setup()
    const onChange = vi.fn()
    render(
      <PlaygroundSpeedTierSelect
        model='gpt-6-sol'
        value='default'
        disabled
        onChange={onChange}
      />
    )
    const control = screen.getByRole('combobox', { name: 'GPT speed tier' })

    expect(control).toBeDisabled()
    await user.click(control)

    expect(screen.queryByRole('listbox')).not.toBeInTheDocument()
    expect(onChange).not.toHaveBeenCalled()
  })
})
