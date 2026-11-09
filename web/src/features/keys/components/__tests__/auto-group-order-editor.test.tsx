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
import { describe, expect, test } from 'vitest'

const { useState } = await import('react')
const { createInstance } = await import('i18next')
const { I18nextProvider, initReactI18next } = await import('react-i18next')
const { AutoGroupOrderEditor } = await import('../auto-group-order-editor')

const i18n = createInstance()
await i18n.use(initReactI18next).init({
  lng: 'en',
  resources: { en: { translation: {} } },
})

const options = [
  { value: 'default', label: 'Default', desc: 'Standard access', ratio: 1 },
  { value: 'vip', label: 'VIP', desc: 'Priority access', ratio: 2 },
  { value: 'team', label: 'Team', desc: 'Shared access', ratio: 3 },
]

function Harness(props: {
  initialGroups?: string[]
  maxCount?: number
  options?: typeof options
}) {
  const [groups, setGroups] = useState(
    props.initialGroups ?? ['default', 'vip']
  )
  return (
    <I18nextProvider i18n={i18n}>
      <AutoGroupOrderEditor
        value={groups}
        options={props.options ?? options}
        maxCount={props.maxCount ?? 2}
        onChange={setGroups}
      />
      <output data-testid='order'>{groups.join(',')}</output>
    </I18nextProvider>
  )
}

describe('Selected groups editor', () => {
  test('enforces the limit and exposes accessible button and keyboard sorting', () => {
    render(<Harness />)
    expect(screen.getByRole('combobox')).toBeDisabled()
    expect(screen.getByText('2 / 2 groups selected')).toBeVisible()
    expect(screen.getByRole('group', { name: 'Selected groups' })).toBeVisible()
    expect(
      screen.getByRole('button', { name: 'Move default up' })
    ).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Move vip down' })).toBeDisabled()

    fireEvent.click(screen.getByRole('button', { name: 'Move default down' }))
    expect(screen.getByTestId('order')).toHaveTextContent('vip,default')
    fireEvent.keyDown(
      screen.getByRole('button', { name: 'Drag vip to reorder' }),
      { key: 'ArrowDown' }
    )
    expect(screen.getByTestId('order')).toHaveTextContent('default,vip')
    fireEvent.keyDown(
      screen.getByRole('button', { name: 'Drag vip to reorder' }),
      { key: 'ArrowUp' }
    )
    expect(screen.getByTestId('order')).toHaveTextContent('vip,default')
    fireEvent.keyDown(
      screen.getByRole('button', { name: 'Drag vip to reorder' }),
      { key: 'ArrowUp' }
    )
    expect(screen.getByTestId('order')).toHaveTextContent('vip,default')
  })

  test('adds and removes explicit groups without an Auto option', () => {
    render(<Harness />)
    fireEvent.click(screen.getByRole('button', { name: 'Remove vip' }))
    expect(screen.getByTestId('order')).toHaveTextContent('default')
    const addButton = screen.getByRole('combobox')
    expect(addButton).toBeEnabled()
    fireEvent.click(addButton)
    expect(
      screen.queryByRole('option', { name: /Standard access/ })
    ).not.toBeInTheDocument()
    fireEvent.click(screen.getByRole('option', { name: /Shared access/ }))
    expect(screen.getByTestId('order')).toHaveTextContent('default,team')
    expect(addButton).toBeDisabled()
    expect(
      screen.queryByRole('button', { name: 'Use global groups' })
    ).not.toBeInTheDocument()
  })

  test('removing the last group leaves an empty explicit selection', () => {
    render(<Harness initialGroups={['default']} />)
    fireEvent.click(screen.getByRole('button', { name: 'Remove default' }))
    expect(screen.getByTestId('order')).toBeEmptyDOMElement()
    expect(screen.getByText('0 / 2 groups selected')).toBeVisible()
    expect(screen.getByRole('combobox')).toBeEnabled()
    expect(
      screen.queryByRole('button', { name: 'Use global groups' })
    ).not.toBeInTheDocument()
  })

  test('starts empty and appends each explicitly added group in order', () => {
    render(<Harness initialGroups={[]} />)
    expect(screen.getByTestId('order')).toBeEmptyDOMElement()
    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(screen.getByRole('option', { name: /Priority access/ }))
    expect(screen.getByTestId('order')).toHaveTextContent(/^vip$/)
    fireEvent.click(screen.getByRole('combobox'))
    fireEvent.click(screen.getByRole('option', { name: /Standard access/ }))
    expect(screen.getByTestId('order')).toHaveTextContent('vip,default')
  })

  test('shows an unavailable stored group and lets the user remove it', () => {
    render(<Harness initialGroups={['revoked', 'default']} />)
    const row = screen
      .getByRole('button', { name: 'Remove revoked' })
      .closest('li')
    if (!row) throw new Error('Expected unavailable group row')
    expect(within(row).getByText('Unavailable')).toBeVisible()
    expect(screen.getByTestId('order')).toHaveTextContent('revoked,default')
    fireEvent.click(screen.getByRole('button', { name: 'Remove revoked' }))
    expect(screen.queryByText('Unavailable')).not.toBeInTheDocument()
    expect(screen.getByTestId('order')).toHaveTextContent(/^default$/)
    expect(screen.getByRole('combobox')).toBeEnabled()
  })

  test('disables adding when no available options remain without dropping stored groups', () => {
    render(<Harness initialGroups={['revoked']} options={[]} />)
    expect(screen.getByRole('combobox')).toBeDisabled()
    expect(screen.getByText('Unavailable')).toBeVisible()
    fireEvent.click(screen.getByRole('button', { name: 'Remove revoked' }))
    expect(screen.getByTestId('order')).toBeEmptyDOMElement()
    expect(screen.getByRole('combobox')).toBeDisabled()
  })

  test('retains an order above a lowered limit until groups are removed', () => {
    render(<Harness initialGroups={['vip', 'default', 'team']} />)
    expect(screen.getByTestId('order')).toHaveTextContent('vip,default,team')
    expect(screen.getByText('3 / 2 groups selected')).toBeVisible()
    expect(screen.getByRole('combobox')).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Remove team' }))
    expect(screen.getByRole('combobox')).toBeDisabled()
    fireEvent.click(screen.getByRole('button', { name: 'Remove default' }))
    expect(screen.getByRole('combobox')).toBeEnabled()
  })
})
