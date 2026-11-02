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
import { describe, expect, it } from 'vitest'

import { buildHomepageDocument, isHomepageMessage } from '../package-document'
import {
  HOMEPAGE_MESSAGE,
  parseHomepagePackage,
  parseHomepageRegistry,
  readPublicHomepagePackage,
  resolveHomepagePackage,
} from '../packages'

const custom = {
  schemaVersion: 1,
  id: 'custom',
  name: 'Landing',
  kind: 'html',
  layout: 'content',
  html: '<main>Hello</main>',
  javascript: 'document.body.dataset.ready = "yes"',
}

describe('Homepage package contract', () => {
  it('imports and exports HTML without dropping its script or configuration', () => {
    const pack = parseHomepagePackage(
      JSON.stringify({ ...custom, config: { title: 'Hello' } })
    )
    expect(pack).toEqual({ ...custom, config: { title: 'Hello' } })
    const doc = new DOMParser().parseFromString(
      buildHomepageDocument(pack, 'https://example.com'),
      'text/html'
    )
    expect(doc.querySelector('main')?.textContent).toBe('Hello')
    expect(doc.querySelector('base')?.target).toBe('_top')
    expect(doc.querySelector('script[src]')?.getAttribute('src')).toMatch(
      /^data:text\/javascript;base64,/
    )
  })
  it('rejects duplicate IDs, unknown active packages and built-in overrides', () => {
    expect(() =>
      parseHomepageRegistry(
        JSON.stringify({
          schemaVersion: 1,
          active: 'custom',
          packages: [custom, custom],
        })
      )
    ).toThrow()
    expect(() =>
      parseHomepageRegistry(
        JSON.stringify({ schemaVersion: 1, active: 'missing', packages: [] })
      )
    ).toThrow()
    expect(() =>
      parseHomepagePackage(JSON.stringify({ ...custom, id: 'official' }))
    ).toThrow()
  })
  it('rejects non-HTTP URL packages and credential-bearing URLs', () => {
    for (const url of [
      'javascript:alert(1)',
      'data:text/html,hello',
      'https://user:password@example.com',
    ]) {
      expect(() =>
        parseHomepagePackage(
          JSON.stringify({
            schemaVersion: 1,
            id: 'link',
            name: 'Link',
            kind: 'url',
            layout: 'full',
            url,
          })
        )
      ).toThrow()
    }
  })
  it('preserves legacy content until a package is explicitly selected', () => {
    expect(parseHomepageRegistry('', '<p>Legacy</p>').active).toBe('legacy')
    expect(readPublicHomepagePackage('<p>Legacy</p>')).toBeNull()
    expect(
      readPublicHomepagePackage(
        JSON.stringify({ schema: 'new-api.homepage', package: custom })
      )
    ).toEqual(custom)
  })
  it('exports CodeQ as an editable package with no response preview', async () => {
    const pack = await resolveHomepagePackage({
      schemaVersion: 1,
      id: 'codeq',
      name: 'CodeQ',
      kind: 'builtin',
      layout: 'content',
    })
    expect(pack.kind).toBe('html')
    if (pack.kind !== 'html') throw new Error('Expected HTML')
    const doc = new DOMParser().parseFromString(pack.html, 'text/html')
    expect(doc.querySelector('.response-panel')).toBeNull()
    expect(doc.querySelectorAll('.model-option')).toHaveLength(4)
    expect(doc.querySelectorAll('.code-tab')).toHaveLength(3)
    const consoleLinks = doc.querySelectorAll(
      '.hero-actions .button-primary, .final-cta .button-primary'
    )
    expect(consoleLinks).toHaveLength(2)
    for (const link of consoleLinks) {
      expect(link.getAttribute('href')).toBe('/dashboard')
      expect(link.getAttribute('target')).toBe('_top')
    }
  })
  it('accepts runtime messages only from the current iframe and channel', () => {
    expect(
      isHomepageMessage(
        new MessageEvent('message', {
          source: window,
          data: { channel: HOMEPAGE_MESSAGE },
        }),
        window
      )
    ).toBe(true)
    expect(
      isHomepageMessage(
        new MessageEvent('message', {
          source: window,
          data: { channel: 'other' },
        }),
        window
      )
    ).toBe(false)
    expect(
      isHomepageMessage(
        new MessageEvent('message', { data: { channel: HOMEPAGE_MESSAGE } }),
        window
      )
    ).toBe(false)
  })
})
