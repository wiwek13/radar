// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setBasename } from '../../api/config'
import { SessionSwitcher } from './SessionSwitcher'
import { useClusterSessions } from './useClusterSessions'

vi.mock('../ui/Tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let container: HTMLDivElement
let requests: { url: string; init?: RequestInit }[]
let sessions: unknown[]

const DEV = 'dev-a1b2c3d4'
const PROD = 'prod-e5f6a7b8'
const QA = 'qa-0a1b2c3d'

function Harness() {
  const s = useClusterSessions()
  return s ? <SessionSwitcher sessions={s} /> : <span>plain switcher</span>
}

beforeEach(() => {
  requests = []
  sessions = [
    { context: 'dev', slug: DEV, state: 'running', rssBytes: 300 * 1024 * 1024 },
    { context: 'prod', slug: PROD, state: 'running' },
    { context: 'qa', slug: QA, state: 'stopped' },
  ]
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    requests.push({ url, init })
    const body = url === '/_sessions/api/state' ? { controller: 'radar-sessions', sessions } : { state: 'stopped' }
    return new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })
  }))
  container = document.createElement('div')
  document.body.appendChild(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => root.unmount())
  container.remove()
  setBasename('')
  vi.unstubAllGlobals()
})

async function render() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () => {
    root.render(<QueryClientProvider client={client}><Harness /></QueryClientProvider>)
  })
  await act(async () => { await new Promise(r => setTimeout(r, 0)) })
}

async function openMenu() {
  await act(async () => { container.querySelector<HTMLButtonElement>('button')!.click() })
}

const button = (label: string) => container.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`)

describe('SessionSwitcher', () => {
  it('stays the plain switcher and asks nothing outside the session controller', async () => {
    for (const base of ['', '/radar', '/s/bad/extra']) {
      setBasename(base)
      await render()
      expect(container.textContent).toBe('plain switcher')
    }
    expect(requests).toHaveLength(0)
  })

  it('lists every cluster, running ones first, with memory', async () => {
    setBasename(`/s/${DEV}`)
    await render()
    await openMenu()
    const text = container.textContent ?? ''
    expect(text.indexOf('Running')).toBeLessThan(text.indexOf('prod'))
    expect(text.indexOf('prod')).toBeLessThan(text.indexOf('Not running'))
    expect(text.indexOf('Not running')).toBeLessThan(text.indexOf('qa'))
    expect(text).toContain('300 MB')
    expect(text).toContain('2 running')
  })

  it('stops another session without moving this tab', async () => {
    setBasename(`/s/${DEV}`)
    await render()
    await openMenu()
    await act(async () => { button('Stop prod')!.click() })
    const post = requests.find(r => r.init?.method === 'POST')!
    expect(post.url).toBe(`/_sessions/api/sessions/${PROD}/stop`)
    expect((post.init?.headers as Record<string, string>)['X-Radar-Sessions']).toBe('1')
    expect(button('Stop qa')).toBeNull()
  })

  it('opens a cluster in a new tab', async () => {
    setBasename(`/s/${DEV}`)
    const open = vi.fn()
    vi.stubGlobal('open', open)
    await render()
    await openMenu()
    await act(async () => { button('Open qa in a new tab')!.click() })
    expect(open).toHaveBeenCalledWith(`/s/${QA}/`, '_blank', 'noopener')
    expect(button(`Open dev in a new tab`)).toBeNull()
  })

  it('will not stop the only running session from its own tab', async () => {
    sessions = [
      { context: 'dev', slug: DEV, state: 'running' },
      { context: 'qa', slug: QA, state: 'stopped' },
    ]
    setBasename(`/s/${DEV}`)
    await render()
    await openMenu()
    expect(button('Stop dev')!.disabled).toBe(true)
    const others = [...container.querySelectorAll('button')].find(b => b.textContent?.startsWith('Stop others'))
    expect(others?.disabled).toBe(true)
  })

  describe('split view', () => {
    const footerButton = (text: string) =>
      [...container.querySelectorAll('button')].find(b => b.textContent?.startsWith(text)) ?? null
    const pick = (name: string) => button(`Show ${name} side by side`)
    const rowButton = (name: string) =>
      [...container.querySelectorAll<HTMLButtonElement>('button')].find(b => b.textContent?.includes(name) && !b.getAttribute('aria-label'))!

    let assign: ReturnType<typeof vi.fn>
    beforeEach(() => {
      sessions.push({ context: 'stage', slug: 'stage-1a2b3c4d', state: 'stopped' })
      assign = vi.fn()
      vi.stubGlobal('location', { ...window.location, assign, href: window.location.href })
    })

    it('keeps the normal dropdown until Split view is pressed', async () => {
      setBasename(`/s/${DEV}`)
      await render()
      await openMenu()
      expect(button('Open qa in a new tab')).not.toBeNull()
      expect(button('Stop prod')).not.toBeNull()
      expect(pick('qa')).toBeNull()
      expect(footerButton('Split view')).not.toBeNull()
    })

    it('ticks this tab and picked rows, refuses a fourth, and opens them in order', async () => {
      setBasename(`/s/${DEV}`)
      await render()
      await openMenu()
      await act(async () => { footerButton('Split view')!.click() })
      expect(pick('dev')!.getAttribute('aria-checked')).toBe('true')
      expect(button('Open qa in a new tab')).toBeNull()
      expect(footerButton('Open side by side')!.disabled).toBe(true)

      await act(async () => { rowButton('qa').click() })
      expect(assign).not.toHaveBeenCalled()
      expect(pick('qa')!.getAttribute('aria-checked')).toBe('true')
      await act(async () => { pick('prod')!.click() })
      expect(pick('stage')!.disabled).toBe(true)
      await act(async () => { rowButton('stage').click() })
      expect(pick('stage')!.getAttribute('aria-checked')).toBe('false')

      await act(async () => { footerButton('Open side by side')!.click() })
      expect(assign).toHaveBeenCalledWith(`/split?s=${DEV},${QA},${PROD}`)
    })

    it('Cancel and closing the menu bring back the normal dropdown', async () => {
      setBasename(`/s/${DEV}`)
      await render()
      await openMenu()
      await act(async () => { footerButton('Split view')!.click() })
      await act(async () => { footerButton('Cancel')!.click() })
      expect(button('Open qa in a new tab')).not.toBeNull()

      await act(async () => { footerButton('Split view')!.click() })
      await act(async () => { document.dispatchEvent(new MouseEvent('mousedown', { bubbles: true })) })
      await openMenu()
      expect(pick('qa')).toBeNull()
      expect(button('Open qa in a new tab')).not.toBeNull()
    })

    it('offers no Split view inside a pane', async () => {
      setBasename(`/s/${DEV}`)
      vi.stubGlobal('top', {})
      await render()
      await openMenu()
      expect(footerButton('Split view')).toBeNull()
    })
  })
})
