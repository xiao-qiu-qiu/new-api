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
import fs from 'node:fs/promises'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const web = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..')
const directory = path.join(web, 'homepage-packages/codeq')
const [manifest, html, css, javascript, locales] = await Promise.all([
  fs.readFile(path.join(directory, 'manifest.json'), 'utf8').then(JSON.parse),
  fs.readFile(path.join(directory, 'index.html'), 'utf8'),
  fs.readFile(path.join(directory, 'style.css'), 'utf8'),
  fs.readFile(path.join(directory, 'app.js'), 'utf8'),
  fs.readFile(path.join(directory, 'locales.json'), 'utf8').then(JSON.parse),
])
const destination = path.join(web, 'src/features/home/packages')
await fs.mkdir(destination, { recursive: true })
await fs.writeFile(
  path.join(destination, 'codeq.json'),
  `${JSON.stringify({ ...manifest, html, css, javascript, locales })}\n`
)
console.log('Built portable CodeQ homepage package')
