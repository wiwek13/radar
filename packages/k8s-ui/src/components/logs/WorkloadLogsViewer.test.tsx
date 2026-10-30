// @vitest-environment jsdom
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { expect, it, vi } from 'vitest'
import { WorkloadLogsViewer, type WorkloadLogsResult } from './WorkloadLogsViewer'
vi.mock('../ui/Toast', () => ({ useToast: () => ({ showError: vi.fn(), showSuccess: vi.fn() }) }))
Object.assign(globalThis, { IS_REACT_ACT_ENVIRONMENT: true })
it('uses a host empty state and disables source controls until a source is read', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  let result: WorkloadLogsResult = { pods: [], logs: [], emptyMessage: 'No instance Pods yet' }
  const stream = vi.fn()
  const fetchAll = vi.fn(async () => result)
  await act(async () => root.render(<WorkloadLogsViewer name="analytics" fetchAll={fetchAll} createStream={stream} disableSourceControlsWithoutSource emptySourceState={<><p>The first instance cannot start.</p><a href="?tab=overview">Overview’s problem</a></>} />))
  expect(host.textContent).toContain('The first instance cannot start')
  expect(host.querySelector('a')?.getAttribute('href')).toBe('?tab=overview')
  expect(host.querySelector('fieldset')?.disabled).toBe(true)
  expect([...host.querySelectorAll('select')].every((el) => el.matches(':disabled'))).toBe(true)
  const streamButton = [...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.textContent === 'Stream')!
  expect(streamButton.disabled).toBe(true)
  act(() => streamButton.click())
  expect(stream).not.toHaveBeenCalled()
  result = { pods: [{ name: 'analytics-1', containers: ['postgres'], ready: true }], logs: [] }
  await act(async () => [...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.querySelector('.lucide-rotate-ccw'))!.click())
  expect(host.querySelector('fieldset')?.disabled).toBe(false)
  expect(streamButton.disabled).toBe(false)
  expect(host.textContent).not.toContain('The first instance cannot start')
  await act(async () => root.unmount())
})

it('keeps streaming available for hosts that wait for Pods to appear', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  await act(async () => root.render(<WorkloadLogsViewer name="pending-job" fetchAll={async () => ({ pods: [], logs: [] })} createStream={vi.fn()} />))
  expect([...host.querySelectorAll<HTMLButtonElement>('button')].find((el) => el.textContent === 'Stream')?.disabled).toBe(false)
  expect(host.querySelector('fieldset')?.disabled).toBe(false)
  await act(async () => root.unmount())
})


it('keeps the all-container choice available by default', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async () => ({ pods: [{ name: 'analytics-1', containers: ['postgres', 'plugin-barman-cloud'], ready: true }], logs: [] }))
  await act(async () => root.render(<WorkloadLogsViewer name="analytics" fetchAll={fetchAll} />))
  const select = host.querySelector<HTMLSelectElement>('select')!
  expect([...select.options].map((option) => option.value)).toEqual(['', 'postgres', 'plugin-barman-cloud'])
  expect(select.options[0].text).toBe('All containers')
  expect(fetchAll).toHaveBeenCalledWith(expect.objectContaining({ container: undefined }))
  await act(async () => root.unmount())
})

it('offers only named containers when the backend cannot read all containers', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async (_params: unknown) => ({ pods: [{ name: 'analytics-1', containers: ['postgres', 'plugin-barman-cloud'], ready: true }], logs: [] }))
  await act(async () => root.render(<WorkloadLogsViewer name="analytics" fetchAll={fetchAll} initialContainer="postgres" allowAllContainers={false} />))
  const select = host.querySelector<HTMLSelectElement>('select')!
  expect([...select.options].map((option) => option.value)).toEqual(['postgres', 'plugin-barman-cloud'])
  expect(host.textContent).not.toContain('All containers')
  expect(select.value).toBe('postgres')
  expect(fetchAll).toHaveBeenCalledWith(expect.objectContaining({ container: 'postgres' }))
  await act(async () => {
    select.value = 'plugin-barman-cloud'
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
  expect(fetchAll).toHaveBeenLastCalledWith(expect.objectContaining({ container: 'plugin-barman-cloud' }))
  await act(async () => root.unmount())
})

const runSelect = (host: HTMLElement) => host.querySelector<HTMLSelectElement>('select[aria-label="Which run to show"]')
async function chooseRun(host: HTMLElement, value: 'current' | 'previous') {
  const select = runSelect(host)!
  await act(async () => {
    select.value = value
    select.dispatchEvent(new Event('change', { bubbles: true }))
  })
}
function fakeStream() {
  return { addEventListener: vi.fn(), close: vi.fn() } as unknown as EventSource
}

it('offers no run choice unless the host asks for it', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  await act(async () => root.render(<WorkloadLogsViewer name="web" fetchAll={async () => ({ pods: [], logs: [] })} />))
  expect(runSelect(host)).toBeNull()
  await act(async () => root.unmount())
})

it('reads previous runs as a snapshot and returns to the live stream', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async (_params: { previous?: boolean }) => ({
    pods: [{ name: 'web-1', containers: ['app'], ready: true }],
    logs: [{ pod: 'web-1', container: 'app', timestamp: '2026-10-30T10:00:00Z', content: 'panic: before the restart' }],
  }))
  const streams: EventSource[] = []
  const createStream = vi.fn(() => { const s = fakeStream(); streams.push(s); return s })
  await act(async () => root.render(<WorkloadLogsViewer name="web" fetchAll={fetchAll} createStream={createStream} autoStream previousRuns />))
  expect(createStream).toHaveBeenCalledTimes(1)
  expect(fetchAll).not.toHaveBeenCalled()

  await chooseRun(host, 'previous')
  expect(streams[0].close).toHaveBeenCalled()
  expect(fetchAll).toHaveBeenLastCalledWith(expect.objectContaining({ previous: true }))
  expect(host.textContent).not.toContain('No logs available')
  expect([...host.querySelectorAll('button')].some((b) => b.textContent === 'Stream')).toBe(false)

  await chooseRun(host, 'current')
  expect(createStream).toHaveBeenCalledTimes(2)
  await act(async () => root.unmount())
})

it('shows the server answer when nothing has restarted', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async (params: { previous?: boolean }) => params.previous
    ? { pods: [{ name: 'web-1', containers: ['app'], ready: true }], logs: [], emptyReason: 'no_previous_run', emptyMessage: 'No container in these pods has restarted.' }
    : { pods: [{ name: 'web-1', containers: ['app'], ready: true }], logs: [] })
  await act(async () => root.render(<WorkloadLogsViewer name="web" fetchAll={fetchAll} previousRuns />))
  await chooseRun(host, 'previous')
  expect(host.textContent).toContain('No container in these pods has restarted.')
  await act(async () => root.unmount())
})

it('reads previous runs by line count only', async () => {
  const host = document.createElement('div'); const root = createRoot(host)
  const fetchAll = vi.fn(async (_params: { previous?: boolean; sinceSeconds?: number; tailLines?: number }) => ({ pods: [{ name: 'web-1', containers: ['app'], ready: true }], logs: [] }))
  await act(async () => root.render(<WorkloadLogsViewer name="web" fetchAll={fetchAll} previousRuns />))
  const range = [...host.querySelectorAll<HTMLSelectElement>('select')].find((el) => [...el.options].some((o) => o.value === 'since:900'))!
  await act(async () => {
    range.value = 'since:900'
    range.dispatchEvent(new Event('change', { bubbles: true }))
  })
  expect(fetchAll).toHaveBeenLastCalledWith(expect.objectContaining({ sinceSeconds: 900 }))
  await chooseRun(host, 'previous')
  expect(fetchAll).toHaveBeenLastCalledWith(expect.objectContaining({ previous: true, tailLines: 100, sinceSeconds: undefined }))
  expect([...host.querySelectorAll('option')].some((o) => o.value === 'since:900')).toBe(false)
  await act(async () => root.unmount())
})

