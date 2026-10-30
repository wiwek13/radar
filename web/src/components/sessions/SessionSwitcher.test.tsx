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
})
