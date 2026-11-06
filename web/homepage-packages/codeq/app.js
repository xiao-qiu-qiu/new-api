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
const t = (key) => window.NewAPIHomepage?.t(key) || key
let apiBase = '/v1'
const models = {
  gpt: { name: 'GPT', id: 'gpt-6.1-sol', icon: 'openai' },
  claude: { name: 'Claude', id: 'claude-sonnet-4-5', icon: 'claude' },
  gemini: { name: 'Gemini', id: 'gemini-2.5-pro', icon: 'gemini' },
  deepseek: { name: 'DeepSeek', id: 'deepseek-chat', icon: 'deepseek' },
}
let selectedModel = 'gpt'
let selectedLanguage = 'python'
let currentCode = ''
let toastTimer
const codeElement = document.querySelector('#code')
const codeTabs = [...document.querySelectorAll('.code-tab')]

function notify(message) {
  const toast = document.querySelector('.toast')
  toast.textContent = t(message)
  toast.classList.add('show')
  clearTimeout(toastTimer)
  toastTimer = setTimeout(() => toast.classList.remove('show'), 2400)
}

function renderCode() {
  const model = models[selectedModel].id
  const snippets = {
    python: `from openai import OpenAI\n\nclient = OpenAI(\n    api_key="YOUR_API_KEY",\n    base_url="${apiBase}"\n)\n\nresponse = client.chat.completions.create(\n    model="${model}",\n    messages=[{\n        "role": "user",\n        "content": "${t('Give me an idea for a weekend project')}"\n    }]\n)`,
    javascript: `import OpenAI from "openai";\n\nconst client = new OpenAI({\n  apiKey: process.env.CODEQ_API_KEY,\n  baseURL: "${apiBase}"\n});\n\nconst response = await client.chat.completions.create({\n  model: "${model}",\n  messages: [{\n    role: "user",\n    content: "${t('Give me an idea for a weekend project')}"\n  }]\n});`,
    curl: `curl ${apiBase}/chat/completions \\\n  -H "Authorization: Bearer $CODEQ_API_KEY" \\\n  -H "Content-Type: application/json" \\\n  -d '{\n    "model": "${model}",\n    "messages": [{\n      "role": "user",\n      "content": "${t('Give me an idea for a weekend project')}"\n    }]\n  }'`,
  }
  currentCode = snippets[selectedLanguage]
  codeElement.replaceChildren()
  const pattern =
    /("[^"\n]*"|'[^'\n]*')|\b(from|import|const|await)\b|\b(OpenAI|curl)\b/g
  let previousEnd = 0
  for (const match of currentCode.matchAll(pattern)) {
    codeElement.append(
      document.createTextNode(currentCode.slice(previousEnd, match.index))
    )
    const span = document.createElement('span')
    if (match[1]) span.className = 'token-string'
    else if (match[2]) span.className = 'token-keyword'
    else span.className = 'token-call'
    span.textContent = match[0]
    codeElement.append(span)
    previousEnd = match.index + match[0].length
  }
  codeElement.append(document.createTextNode(currentCode.slice(previousEnd)))
  document
    .querySelector('#code-panel')
    .setAttribute('aria-labelledby', `tab-${selectedLanguage}`)
  codeTabs.forEach((tab) => {
    const selected = tab.dataset.language === selectedLanguage
    tab.setAttribute('aria-selected', String(selected))
    tab.tabIndex = selected ? 0 : -1
  })
}

function selectModel(key) {
  selectedModel = key
  document.querySelectorAll('.model-option').forEach((button) => {
    const selected = button.dataset.model === key
    button.setAttribute('aria-pressed', String(selected))
    button.classList.toggle('selected', selected)
  })
  renderCode()
}

async function copyText(text, successMessage) {
  try {
    if (window.NewAPIHomepage) {
      if (!(await window.NewAPIHomepage.copy(text))) {
        throw new Error('Copy unavailable')
      }
    } else await navigator.clipboard.writeText(text)
    notify(successMessage)
  } catch {
    if (text === currentCode) {
      const range = document.createRange()
      range.selectNodeContents(codeElement)
      const selection = window.getSelection()
      selection.removeAllRanges()
      selection.addRange(range)
      notify('Code selected. Press Ctrl+C to copy.')
      return
    }
    notify(`Copy this endpoint manually: ${apiBase}`)
  }
}

document
  .querySelectorAll('.model-option')
  .forEach((button) =>
    button.addEventListener('click', () => selectModel(button.dataset.model))
  )
document.querySelectorAll('[data-select]').forEach((button) =>
  button.addEventListener('click', () => {
    selectModel(button.dataset.select)
    window.NewAPIHomepage?.scrollTo(
      document.querySelector('#workspace').offsetTop
    )
  })
)
codeTabs.forEach((tab, index) => {
  tab.addEventListener('click', () => {
    selectedLanguage = tab.dataset.language
    renderCode()
  })
  tab.addEventListener('keydown', (event) => {
    let next = index
    if (event.key === 'ArrowRight') next = (index + 1) % codeTabs.length
    else if (event.key === 'ArrowLeft') {
      next = (index - 1 + codeTabs.length) % codeTabs.length
    } else if (event.key === 'Home') next = 0
    else if (event.key === 'End') next = codeTabs.length - 1
    else return
    event.preventDefault()
    codeTabs[next].focus()
    codeTabs[next].click()
  })
})
document
  .querySelector('.copy-code')
  .addEventListener('click', () => copyText(currentCode, 'Example code copied'))
document
  .querySelector('.endpoint-copy')
  .addEventListener('click', () => copyText(apiBase, 'API endpoint copied'))
renderCode()

addEventListener('newapi:context', (event) => {
  const context = event.detail
  const config = context.config || {}
  apiBase =
    typeof config.apiBase === 'string' && config.apiBase
      ? config.apiBase
      : context.apiBase
  for (const [key, model] of Object.entries(models)) {
    if (typeof config.models?.[key] === 'string') model.id = config.models[key]
  }
  document.querySelectorAll('.diagram-hub img').forEach((image) => {
    image.src = context.logo || '/logo.png'
  })
  document.querySelectorAll('.diagram-hub span').forEach((node) => {
    node.textContent = config.brand || context.siteName
  })
  document.querySelectorAll('.endpoint-copy span').forEach((node) => {
    node.textContent = apiBase
  })
  if (config.headline) {
    document.querySelector('h1').textContent = config.headline
  }
  if (config.subtitle) {
    document.querySelector('.hero-description').textContent = config.subtitle
  }
  renderCode()
  document.documentElement.classList.add('motion-ready')
})
document.querySelectorAll('a[href^="#"]').forEach((link) =>
  link.addEventListener('click', (event) => {
    const section = document.querySelector(link.getAttribute('href'))
    if (section && window.NewAPIHomepage) {
      event.preventDefault()
      window.NewAPIHomepage.scrollTo(section.offsetTop)
    }
  })
)

if (!window.NewAPIHomepage) {
  document.documentElement.classList.add('motion-ready')
}

// The observer respects the host viewport even inside the homepage iframe.
const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)')
if (!reducedMotion.matches && 'IntersectionObserver' in window) {
  const revealTargets = document.querySelectorAll(
    '.section-heading, .feature-card, .catalog-card, .step, .faq-heading, .faq-list, .final-cta'
  )
  const revealObserver = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) {
          entry.target.dataset.reveal = 'visible'
          revealObserver.unobserve(entry.target)
        }
      }
    },
    { threshold: 0.08 }
  )
  for (const target of revealTargets) {
    target.dataset.reveal = 'pending'
    revealObserver.observe(target)
    target.addEventListener(
      'focusin',
      () => {
        target.dataset.reveal = 'visible'
        revealObserver.unobserve(target)
      },
      { once: true }
    )
  }
  reducedMotion.addEventListener('change', (event) => {
    if (!event.matches) return
    revealObserver.disconnect()
    for (const target of revealTargets) target.dataset.reveal = 'visible'
  })
}

const pointerMotion = matchMedia(
  '(hover: hover) and (pointer: fine) and (prefers-reduced-motion: no-preference)'
)
const pointerLayer = document.createElement('div')
pointerLayer.className = 'pointer-glow-layer'
pointerLayer.setAttribute('aria-hidden', 'true')
const pointerGlow = document.createElement('div')
pointerGlow.className = 'pointer-glow'
pointerLayer.append(pointerGlow)
document.body.append(pointerLayer)
let pointerFrame = 0
let pointerFadeTimer
let pointerX = 0
let pointerY = 0
document.addEventListener(
  'pointermove',
  (event) => {
    if (!pointerMotion.matches || event.pointerType === 'touch') return
    pointerX = event.clientX
    pointerY = event.clientY
    clearTimeout(pointerFadeTimer)
    pointerFadeTimer = setTimeout(hidePointerGlow, 800)
    if (pointerFrame) return
    pointerFrame = requestAnimationFrame(() => {
      pointerGlow.style.translate = `${pointerX}px ${pointerY}px`
      pointerLayer.classList.add('is-active')
      pointerFrame = 0
    })
  },
  { passive: true }
)
const hidePointerGlow = () => {
  clearTimeout(pointerFadeTimer)
  cancelAnimationFrame(pointerFrame)
  pointerFrame = 0
  pointerLayer.classList.remove('is-active')
}
document.documentElement.addEventListener('pointerleave', hidePointerGlow)
addEventListener('blur', hidePointerGlow)
addEventListener('wheel', hidePointerGlow, { passive: true })
pointerMotion.addEventListener('change', hidePointerGlow)
