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
import { act, fireEvent, render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import i18next from 'i18next'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'

import { RankingMetricSwitch } from '../components/ranking-metric-switch'

let bounds: DOMRect
let intersect: (visible: boolean) => void
let resize: () => void

beforeEach(() => {
  bounds = new DOMRect(1016.5, 300, 247.25, 44)
  vi.spyOn(HTMLElement.prototype, 'getBoundingClientRect').mockImplementation(
    function (this: HTMLElement) {
      // The surrounding anchor can be wider than the actual toggle group.
      if (this.dataset.slot === 'toggle-group') return bounds
      return new DOMRect(bounds.x, bounds.y, Math.max(240, bounds.width), 44)
    }
  )
  vi.stubGlobal(
    'IntersectionObserver',
    class {
      constructor(callback: IntersectionObserverCallback) {
        intersect = (visible) => {
          callback(
            [
              {
                isIntersecting: visible,
                boundingClientRect: bounds,
              } as IntersectionObserverEntry,
            ],
            this as unknown as IntersectionObserver
          )
        }
      }
      observe() {}
      disconnect() {}
    }
  )
  vi.stubGlobal(
    'ResizeObserver',
    class {
      constructor(callback: ResizeObserverCallback) {
        resize = () => callback([], this as unknown as ResizeObserver)
      }
      observe() {}
      disconnect() {}
    }
  )
})

afterEach(async () => {
  vi.unstubAllGlobals()
  await act(() => i18next.changeLanguage('en'))
  i18next.removeResourceBundle('fr', 'translation')
})

test.each([
  { viewport: 'desktop', left: 1016.5, width: 247.25 },
  { viewport: 'mobile', left: 12, width: 218.5 },
])(
  'floating on $viewport preserves the original control bounds and live placeholder',
  (scenario) => {
    bounds = new DOMRect(scenario.left, 300, scenario.width, 44)
    const view = render(
      <RankingMetricSwitch metric='tokens' onChange={vi.fn()} />
    )
    const original = screen.getByRole('group', { name: 'User ranking metric' })
    bounds.y = -50
    act(() => intersect(false))

    const floating = screen.getByRole('group', { name: 'User ranking metric' })
    expect(floating.closest('.fixed')).toHaveStyle({
      left: `${scenario.left}px`,
      width: `${scenario.width}px`,
    })
    expect(view.container).not.toContainElement(floating)
    expect(original).toBeInTheDocument()
    expect(original).not.toBeVisible()
    expect(original.closest('[inert]')).toHaveAttribute('aria-hidden', 'true')

    bounds.y = 300
    act(() => intersect(true))
    expect(screen.getByRole('group', { name: 'User ranking metric' })).toBe(
      original
    )
    expect(original).toBeVisible()
    expect(document.querySelector('.fixed')).not.toBeInTheDocument()
  }
)

test('a floating control tracks viewport resize and translated or font-driven size changes', async () => {
  render(<RankingMetricSwitch metric='tokens' onChange={vi.fn()} />)
  bounds.y = -50
  act(() => intersect(false))

  bounds = new DOMRect(12, -50, 218.5, 44)
  fireEvent(window, new Event('resize'))
  expect(screen.getByRole('group').closest('.fixed')).toHaveStyle({
    left: '12px',
    width: '218.5px',
  })

  i18next.addResourceBundle('fr', 'translation', {
    'Token leaderboard': 'Classement des jetons',
  })
  await act(() => i18next.changeLanguage('fr'))
  bounds = new DOMRect(24, -50, 298.75, 44)
  act(() => resize())
  expect(screen.getByRole('group').closest('.fixed')).toHaveStyle({
    left: '24px',
    width: '298.75px',
  })
  expect(
    screen.getByRole('button', { name: 'Classement des jetons' })
  ).toBeVisible()
})

test('switching a floating metric keeps its position and invokes the existing change callback', async () => {
  const onChange = vi.fn()
  const scroll = vi.spyOn(window, 'scrollTo')
  const view = render(
    <RankingMetricSwitch metric='tokens' onChange={onChange} />
  )
  bounds.y = -50
  act(() => intersect(false))
  const floating = screen.getByRole('group').closest('.fixed')
  await userEvent.click(
    screen.getByRole('button', { name: 'Spending leaderboard' })
  )
  expect(onChange).toHaveBeenCalledExactlyOnceWith('spending')
  view.rerender(<RankingMetricSwitch metric='spending' onChange={onChange} />)
  expect(screen.getByRole('group').closest('.fixed')).toBe(floating)
  expect(
    screen.getByRole('button', { name: 'Spending leaderboard' })
  ).toHaveAttribute('aria-pressed', 'true')
  expect(within(view.container).queryByRole('button')).not.toBeInTheDocument()
  expect(scroll).not.toHaveBeenCalled()
})
