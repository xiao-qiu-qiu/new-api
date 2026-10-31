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
export function applyFaviconToDom(url: string) {
  if (typeof document === 'undefined') return
  try {
    const existing =
      document.querySelectorAll<HTMLLinkElement>('link[rel~="icon"]')
    const siteFavicon = document.querySelector<HTMLLinkElement>(
      'link[rel~="icon"][data-site-favicon]'
    )
    if (siteFavicon) {
      existing.forEach((link) => {
        if (link !== siteFavicon) link.remove()
      })
      return
    }
    if (!url) return
    const next = new URL(url, window.location.href).href
    if (existing.length === 1 && existing[0].href === next) return
    const link = document.createElement('link')
    link.rel = 'icon'
    link.href = url
    existing.forEach((l) => l.remove())
    document.head.appendChild(link)
  } catch {
    // Ignore malformed URLs
  }
}
