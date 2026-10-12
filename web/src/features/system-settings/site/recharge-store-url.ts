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
export function isValidRechargeStoreURL(rawURL: string): boolean {
  const value = rawURL.trim()
  if (!/^https?:\/\//i.test(value) || /[\s\\]/u.test(value)) return false
  for (const character of value) {
    const codePoint = character.codePointAt(0) ?? 0
    if (codePoint <= 0x1f || codePoint === 0x7f) return false
  }

  try {
    const url = new URL(value)
    const authority = value.slice(value.indexOf('//') + 2).split(/[/?#]/u, 1)[0]
    return (
      (url.protocol === 'https:' || url.protocol === 'http:') &&
      url.hostname.length > 0 &&
      !authority.includes('@') &&
      url.username === '' &&
      url.password === ''
    )
  } catch {
    return false
  }
}
