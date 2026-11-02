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
import { HOMEPAGE_MESSAGE, type HomepagePackage } from './packages'

// This bridge runs inside an opaque-origin sandbox, never in the console DOM.
const bridge = `(() => {
  const channel = ${JSON.stringify('new-api.homepage/v1')};
  const pending = new Map(); let nextId = 0;
  const originals = new WeakMap();
  const attributeOriginals = new WeakMap();
  const send = (type, data = {}) => parent.postMessage({ channel, type, ...data }, '*');
  window.NewAPIHomepage = {
    context: null,
    t: key => window.NewAPIHomepage.context?.messages?.[key] || key,
    copy: text => new Promise(resolve => {
      const requestId = String(++nextId);
      const timer = setTimeout(() => { pending.delete(requestId); resolve(false); }, 3000);
      pending.set(requestId, value => { clearTimeout(timer); resolve(value); });
      send('copy', { text, requestId });
    }),
    resize: () => send('resize', { height: Math.ceil(document.body.getBoundingClientRect().height) }),
    scrollTo: top => send('scroll', { top }),
  };
  addEventListener('message', event => {
    if (event.source !== parent || event.data?.channel !== channel) return;
    if (event.data.type === 'context') {
      const context = event.data.context;
      window.NewAPIHomepage.context = context;
      document.documentElement.classList.toggle('dark', context.theme === 'dark');
      document.documentElement.dataset.theme = context.theme;
      document.documentElement.lang = context.language;
      const texts = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
      while (texts.nextNode()) {
        const node = texts.currentNode;
        if (node.parentElement.closest('script, style, pre, code')) continue;
        if (!originals.has(node)) originals.set(node, node.textContent);
        const original = originals.get(node);
        const key = original.trim();
        if (key) node.textContent = original.replace(key, context.messages?.[key] || key);
      }
      for (const node of document.querySelectorAll('[aria-label], [title], [alt]')) {
        if (!attributeOriginals.has(node)) attributeOriginals.set(node, {});
        const attributes = attributeOriginals.get(node);
        for (const attr of ['aria-label', 'title', 'alt']) {
          const key = attributes[attr] || node.getAttribute(attr);
          if (key) { attributes[attr] = key; node.setAttribute(attr, context.messages?.[key] || key); }
        }
      }
      for (const [name, value] of Object.entries(context.tokens || {})) document.documentElement.style.setProperty(name, value);
      dispatchEvent(new CustomEvent('newapi:context', { detail: context }));
    }
    if (event.data.type === 'copied') { pending.get(event.data.requestId)?.(event.data.success); pending.delete(event.data.requestId); }
  });
  addEventListener('DOMContentLoaded', () => {
    send('ready');
    let frame;
    new ResizeObserver(() => { cancelAnimationFrame(frame); frame = requestAnimationFrame(window.NewAPIHomepage.resize); }).observe(document.body);
    window.NewAPIHomepage.resize();
  });
})();`

export function buildHomepageDocument(
  pack: HomepagePackage,
  origin: string
): string {
  if (pack.kind !== 'html') return ''
  const doc = new DOMParser().parseFromString(pack.html, 'text/html')
  const base = doc.createElement('base')
  base.href = `${origin}/`
  base.target = '_top'
  if (!doc.head.querySelector('base')) doc.head.prepend(base)
  const runtime = doc.createElement('script')
  runtime.textContent = bridge
  doc.head.prepend(runtime)
  const viewport = doc.createElement('meta')
  viewport.name = 'viewport'
  viewport.content = 'width=device-width, initial-scale=1'
  if (!doc.head.querySelector('meta[name="viewport"]')) {
    doc.head.append(viewport)
  }
  const style = doc.createElement('style')
  style.textContent = pack.css ?? ''
  doc.head.append(style)
  if (pack.javascript) {
    const script = doc.createElement('script')
    const bytes = new TextEncoder().encode(pack.javascript)
    let binary = ''
    for (const byte of bytes) binary += String.fromCharCode(byte)
    script.src = `data:text/javascript;base64,${btoa(binary)}`
    doc.body.append(script)
  }
  return `<!doctype html>\n${doc.documentElement.outerHTML}`
}

export function isHomepageMessage(
  event: MessageEvent,
  frame: Window | null
): boolean {
  return (
    !!frame &&
    event.source === frame &&
    event.data?.channel === HOMEPAGE_MESSAGE
  )
}
