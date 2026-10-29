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
import { useForm } from 'react-hook-form'
import { describe, expect, test } from 'vitest'

import { Form } from '@/components/ui/form'

import {
  CHANNEL_FORM_DEFAULT_VALUES,
  transformChannelToFormDefaults,
  transformFormDataToCreatePayload,
  transformFormDataToUpdatePayload,
  type ChannelFormValues,
} from '../../lib/channel-form'
import { channelSchema } from '../../types'
import { ChannelServiceTiersSetting } from '../channel-service-tiers-setting'

function SettingsForm(props: {
  values?: Partial<ChannelFormValues>
  disabled?: boolean
  onSave?: (values: ChannelFormValues) => void
}) {
  const form = useForm<ChannelFormValues>({
    defaultValues: {
      ...CHANNEL_FORM_DEFAULT_VALUES,
      name: 'GPT upstream',
      key: 'test-key',
      models: 'gpt-6-astra',
      ...props.values,
    },
  })
  return (
    <Form {...form}>
      <form onSubmit={form.handleSubmit((values) => props.onSave?.(values))}>
        <ChannelServiceTiersSetting disabled={props.disabled} />
        <button type='submit'>Save</button>
      </form>
    </Form>
  )
}

describe('channel speed tier capabilities', () => {
  test('selects supported tiers, keeps at least one, and saves create/update payloads', async () => {
    const user = userEvent.setup()
    let saved: ChannelFormValues | undefined
    render(
      <SettingsForm
        onSave={(values) => {
          saved = values
        }}
      />
    )
    const toggle = screen.getByRole('switch', { name: 'Limit GPT speed tiers' })
    expect(toggle).not.toBeChecked()
    expect(screen.queryByRole('checkbox')).not.toBeInTheDocument()
    await user.click(toggle)
    expect(screen.getByRole('checkbox', { name: 'Standard' })).toBeChecked()
    expect(screen.getByRole('checkbox', { name: 'Fast tier' })).toBeChecked()
    await user.click(screen.getByRole('checkbox', { name: 'Ultrafast tier' }))
    await user.click(screen.getByRole('checkbox', { name: 'Standard' }))
    await user.click(screen.getByRole('checkbox', { name: 'Fast tier' }))
    expect(
      screen.getByRole('checkbox', { name: 'Ultrafast tier' })
    ).toHaveAttribute('aria-disabled', 'true')
    await user.click(screen.getByRole('checkbox', { name: 'Ultrafast tier' }))
    expect(
      screen.getByRole('checkbox', { name: 'Ultrafast tier' })
    ).toBeChecked()
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(saved).toBeDefined()
    if (!saved) throw new Error('Form was not submitted')
    for (const payload of [
      transformFormDataToCreatePayload(saved).channel,
      transformFormDataToUpdatePayload(saved, 1),
    ]) {
      expect(
        JSON.parse(payload.settings || '{}').supported_service_tiers
      ).toEqual(['ultrafast'])
    }
  })

  test.each([
    ['deepseek-v4', '', '', false],
    ['gpt-4o-audio-preview,gpt-image-2', '', '', false],
    ['friendly-alias', '{"friendly-alias":"openai/gpt-6-astra"}', '', true],
    [
      'friendly-alias',
      '{"friendly-alias":"middle","middle":"openai/gpt-6-astra"}',
      '',
      true,
    ],
    ['gpt-6-astra', '{"gpt-6-astra":"claude-opus"}', '', false],
    ['friendly-alias', '', '{"model":"gpt-6-astra"}', true],
  ])(
    'shows capabilities for the mapped GPT model: %s',
    (models, model_mapping, param_override, visible) => {
      render(
        <SettingsForm values={{ models, model_mapping, param_override }} />
      )
      const toggle = screen.queryByRole('switch', {
        name: 'Limit GPT speed tiers',
      })
      if (visible) expect(toggle).toBeInTheDocument()
      else expect(toggle).not.toBeInTheDocument()
    }
  )

  test('loads saved capabilities and removes only their restriction when switched off', async () => {
    const user = userEvent.setup()
    const channel = channelSchema.parse({
      id: 1,
      name: 'GPT upstream',
      key: '',
      type: 1,
      models: 'gpt-6-astra',
      status: 1,
      created_time: 0,
      test_time: 0,
      response_time: 0,
      balance_updated_time: 0,
      settings:
        '{"supported_service_tiers":["default","priority","ultrafast"],"custom_setting":"keep"}',
    })
    const values = transformChannelToFormDefaults(channel)
    expect(values.supported_service_tiers).toEqual([
      'default',
      'priority',
      'ultrafast',
    ])
    let saved: ChannelFormValues | undefined
    render(
      <SettingsForm
        values={values}
        onSave={(next) => {
          saved = next
        }}
      />
    )
    expect(
      screen.getByRole('checkbox', { name: 'Ultrafast tier' })
    ).toBeChecked()
    await user.click(
      screen.getByRole('switch', { name: 'Limit GPT speed tiers' })
    )
    await user.click(screen.getByRole('button', { name: 'Save' }))
    expect(saved).toBeDefined()
    if (!saved) throw new Error('Form was not submitted')
    const settings = JSON.parse(
      transformFormDataToUpdatePayload(saved, 1).settings || '{}'
    )
    expect(settings).not.toHaveProperty('supported_service_tiers')
    expect(settings.custom_setting).toBe('keep')
  })

  test('prevents capability changes without sensitive channel permission', () => {
    render(
      <SettingsForm
        disabled
        values={{ supported_service_tiers: ['default', 'priority'] }}
      />
    )
    expect(
      screen.getByRole('switch', { name: 'Limit GPT speed tiers' })
    ).toHaveAttribute('aria-disabled', 'true')
    for (const checkbox of screen.getAllByRole('checkbox')) {
      expect(checkbox).toHaveAttribute('aria-disabled', 'true')
    }
  })
})
