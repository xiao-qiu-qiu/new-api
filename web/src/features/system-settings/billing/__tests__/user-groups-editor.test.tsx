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
import { fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { useState } from 'react'
import { expect, test, vi } from 'vitest'

import { UserGroupsEditor } from '../../models/user-groups-editor'

function Fixture() {
  const [values, setValues] = useState({
    UserGroups: '{"members":"standard"}',
    TopupGroupRatio: '{"members":1.25,"legacy":2}',
  })
  return (
    <>
      <UserGroupsEditor
        userGroups={values.UserGroups}
        topupGroupRatio={values.TopupGroupRatio}
        onChange={(field, value) =>
          setValues((previous) => ({ ...previous, [field]: value }))
        }
      />
      <output data-testid='values'>{JSON.stringify(values)}</output>
    </>
  )
}

test('only user groups appear, existing names are immutable and zero recharge ratios are preserved', () => {
  render(<Fixture />)
  expect(screen.getByText('members')).toBeInTheDocument()
  expect(screen.queryByText('legacy')).not.toBeInTheDocument()
  expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
  fireEvent.change(screen.getByRole('spinbutton'), {
    target: { value: '0' },
  })
  const values = JSON.parse(screen.getByTestId('values').textContent ?? '')
  expect(JSON.parse(values.TopupGroupRatio)).toEqual({
    members: 0,
    legacy: 2,
  })
  expect(JSON.parse(values.UserGroups)).toEqual({ members: 'standard' })
})

test('adding a named group and confirming deletion only changes the user group configuration', async () => {
  const user = userEvent.setup()
  render(<Fixture />)
  await user.click(screen.getByRole('button', { name: 'Add user group' }))
  const dialog = screen.getByRole('dialog')
  await user.type(
    within(dialog).getByRole('textbox', { name: 'User group name' }),
    'new-members'
  )
  await user.click(within(dialog).getByRole('button', { name: 'Add' }))
  expect(screen.getByText('new-members')).toBeInTheDocument()
  expect(
    JSON.parse(
      JSON.parse(screen.getByTestId('values').textContent ?? '').UserGroups
    )
  ).toEqual({ members: 'standard', 'new-members': '' })
  await user.click(
    screen.getByRole('button', { name: 'Delete user group new-members' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  )
  expect(screen.queryByText('new-members')).not.toBeInTheDocument()
  const values = JSON.parse(screen.getByTestId('values').textContent ?? '')
  expect(JSON.parse(values.UserGroups)).toEqual({ members: 'standard' })
  expect(JSON.parse(values.TopupGroupRatio)).toEqual({
    members: 1.25,
    legacy: 2,
  })
})

test.each(['null', '[]', '{"members":null}'])(
  'invalid user group JSON %s displays an error without overwriting configuration',
  (userGroups) => {
    const onChange = vi.fn()
    render(
      <UserGroupsEditor
        userGroups={userGroups}
        topupGroupRatio='{}'
        onChange={onChange}
      />
    )
    expect(screen.getByRole('alert')).toBeInTheDocument()
    expect(onChange).not.toHaveBeenCalled()
  }
)

test('typing decimal zeros preserves the draft and clearing restores the default without storing a string', () => {
  render(<Fixture />)
  const input = screen.getByRole('spinbutton') as HTMLInputElement
  fireEvent.change(input, { target: { value: '0.0' } })
  expect(input.value).toBe('0.0')
  fireEvent.change(input, { target: { value: '0.0001' } })
  expect(
    JSON.parse(
      JSON.parse(screen.getByTestId('values').textContent ?? '').TopupGroupRatio
    ).members
  ).toBe(0.0001)
  fireEvent.change(input, { target: { value: '' } })
  expect(
    JSON.parse(
      JSON.parse(screen.getByTestId('values').textContent ?? '').TopupGroupRatio
    )
  ).toEqual({ legacy: 2 })
  fireEvent.blur(input)
  expect(input.value).toBe('1')
})

test.each(['-0.5'])(
  'negative recharge ratio %s stays visibly invalid until corrected',
  (value) => {
    render(<Fixture />)
    const input = screen.getByRole('spinbutton')
    fireEvent.change(input, { target: { value } })
    expect(input).toHaveAttribute('aria-invalid', 'true')
    fireEvent.change(input, { target: { value: '0.5' } })
    expect(input).toHaveAttribute('aria-invalid', 'false')
  }
)

test('duplicate and reserved names are rejected without creating a group', async () => {
  const user = userEvent.setup()
  render(<Fixture />)
  await user.click(screen.getByRole('button', { name: 'Add user group' }))
  const input = screen.getByRole('textbox', { name: 'User group name' })
  for (const name of ['members', 'auto', '*']) {
    await user.clear(input)
    await user.type(input, name)
    await user.click(
      within(screen.getByRole('dialog')).getByRole('button', { name: 'Add' })
    )
    expect(await screen.findByRole('alert')).toBeInTheDocument()
    expect(
      JSON.parse(
        JSON.parse(screen.getByTestId('values').textContent ?? '').UserGroups
      )
    ).toEqual({ members: 'standard' })
  }
})

test('deletion removes its recharge override while retaining unrelated overrides', async () => {
  const user = userEvent.setup()
  render(<Fixture />)
  await user.click(
    screen.getByRole('button', { name: 'Delete user group members' })
  )
  await user.click(
    within(screen.getByRole('alertdialog')).getByRole('button', {
      name: 'Delete',
    })
  )
  expect(
    JSON.parse(
      JSON.parse(screen.getByTestId('values').textContent ?? '').TopupGroupRatio
    )
  ).toEqual({ legacy: 2 })
  expect(screen.getByRole('table', { name: 'User groups' })).toBeInTheDocument()
})

test('identity editing exposes no routing selector and preserves legacy mappings when recharge ratios change', () => {
  render(<Fixture />)
  expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
  expect(screen.queryByText('Default billing group')).not.toBeInTheDocument()
  fireEvent.change(screen.getByRole('spinbutton'), {
    target: { value: '0.5' },
  })
  const values = JSON.parse(screen.getByTestId('values').textContent ?? '')
  expect(JSON.parse(values.UserGroups)).toEqual({ members: 'standard' })
  expect(JSON.parse(values.TopupGroupRatio)).toEqual({
    members: 0.5,
    legacy: 2,
  })
})

test('a pure identity can be created with no multiplier groups by keyboard', async () => {
  const user = userEvent.setup()
  const onChange = vi.fn()
  render(
    <UserGroupsEditor
      userGroups='{}'
      topupGroupRatio='{}'
      onChange={onChange}
    />
  )
  const add = screen.getByRole('button', { name: 'Add user group' })
  expect(add).toBeEnabled()
  expect(screen.getByText('No user groups')).toBeInTheDocument()
  add.focus()
  await user.keyboard('{Enter}')
  await user.type(
    screen.getByRole('textbox', { name: 'User group name' }),
    'members{Enter}'
  )
  expect(onChange).toHaveBeenCalledOnce()
  expect(onChange.mock.calls[0][0]).toBe('UserGroups')
  expect(JSON.parse(onChange.mock.calls[0][1])).toEqual({ members: '' })
})
