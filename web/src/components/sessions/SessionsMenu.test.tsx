// @vitest-environment jsdom
import { act, type ReactNode } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setBasename } from '../../api/config'
import { SessionsMenu } from './SessionsMenu'

vi.mock('../ui/Tooltip', () => ({ Tooltip: ({ children }: { children: ReactNode }) => children }))

Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })

let root: Root
let container: HTMLDivElement
let requests: { url: string; init?: RequestInit }[]
let stateBody: unknown

const DEV = 'dev-a1b2c3d4'
const PROD = 'prod-e5f6a7b8'

beforeEach(() => {
  requests = []
  stateBody = {
    controller: 'radar-sessions',
    sessions: [
      { context: 'dev', slug: DEV, state: 'running', rssBytes: 300 * 1024 * 1024 },
      { context: 'prod', slug: PROD, state: 'running' },
    ],
  }
  vi.stubGlobal('fetch', vi.fn(async (url: string, init?: RequestInit) => {
    requests.push({ url, init })
    const body = url === '/_sessions/api/state' ? stateBody : { slug: PROD, state: 'stopped' }
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

async function renderMenu() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () => {
    root.render(<QueryClientProvider client={client}><SessionsMenu /></QueryClientProvider>)
  })
  await act(async () => { await new Promise(r => setTimeout(r, 0)) })
}

describe('SessionsMenu', () => {
  it('renders nothing and asks nothing outside the session controller', async () => {
    for (const base of ['', '/radar', '/s/bad/extra']) {
      setBasename(base)
      await renderMenu()
      expect(container.innerHTML).toBe('')
    }
    expect(requests).toEqual([])
  })

  it('hides when another server answers the state path', async () => {
    setBasename(`/s/${DEV}`)
    stateBody = { sessions: [] }
    await renderMenu()
    expect(container.innerHTML).toBe('')
  })

  it('lists sessions and marks this tab', async () => {
    setBasename(`/s/${DEV}`)
    await renderMenu()
    const trigger = container.querySelector('button[aria-label="Cluster sessions"]') as HTMLButtonElement
    expect(trigger.textContent).toBe('2')
    await act(async () => trigger.click())
    expect(container.textContent).toContain('this tab')
    expect(container.textContent).toContain('300 MB')
    expect(container.querySelector('[aria-label="Open dev in a new tab"]')).toBeNull()
    expect(container.querySelector('[aria-label="Open prod in a new tab"]')).not.toBeNull()
  })

  it('stops another session through the controller with the CSRF header', async () => {
    setBasename(`/s/${DEV}`)
    await renderMenu()
    await act(async () => (container.querySelector('button[aria-label="Cluster sessions"]') as HTMLButtonElement).click())
    await act(async () => (container.querySelector('[aria-label="Stop prod"]') as HTMLButtonElement).click())
    const stop = requests.find(r => r.url === `/_sessions/api/sessions/${PROD}/stop`)
    expect(stop?.init?.method).toBe('POST')
    expect((stop?.init?.headers as Record<string, string>)['X-Radar-Sessions']).toBe('1')
  })
})
