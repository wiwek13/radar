import { useEffect, useMemo, useRef, useState, type MouseEvent } from 'react'
import { clsx } from 'clsx'
import { ExternalLink, Layers, Loader2, Square } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { StatusDot, type StatusTone } from '@skyhook-io/k8s-ui'
import { getBasename } from '../../api/config'
import { readErrorBody } from '../../api/httpErrors'
import { useAnimatedUnmount } from '../../hooks/useAnimatedUnmount'
import { TRANSITION_MENU, overlayExitMs, overlayTransitionStyle } from '../../utils/animation'
import { Tooltip } from '../ui/Tooltip'

type SessionState = 'stopped' | 'starting' | 'running' | 'stopping' | 'failed'

interface ClusterSession {
  context: string
  slug: string
  state: SessionState
  rssBytes?: number
  error?: string
}

interface SessionsState {
  controller: string
  sessions: ClusterSession[]
}

// The session controller (cmd/radar-sessions) serves each cluster's Radar
// under /s/<slug>/ and answers its own API at the origin root. These calls go
// to the controller, not to this Radar, so they deliberately bypass
// getApiBase(). Outside the controller the basename never matches and the
// menu makes no request at all.
const SESSION_BASENAME = /^\/s\/([a-z0-9]+(?:-[a-z0-9]+)*)$/
const CONTROLLER_NAME = 'radar-sessions'

const STATE_TONE: Record<SessionState, StatusTone> = {
  running: 'healthy',
  starting: 'degraded',
  stopping: 'degraded',
  failed: 'unhealthy',
  stopped: 'unknown',
}

const sessionPath = (slug: string) => `/s/${slug}/`

async function controllerPost(path: string, body?: unknown) {
  const resp = await fetch(`/_sessions/api/${path}`, {
    method: 'POST',
    headers: { 'X-Radar-Sessions': '1', 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!resp.ok) {
    const err = await readErrorBody(resp)
    throw new Error(err.error || `HTTP ${resp.status}`)
  }
  return resp.json()
}

function formatMemory(bytes?: number) {
  if (!bytes) return ''
  const mb = bytes / (1024 * 1024)
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`
}

export function SessionsMenu() {
  const currentSlug = useMemo(() => SESSION_BASENAME.exec(getBasename())?.[1], [])
  const [open, setOpen] = useState(false)
  const [filter, setFilter] = useState('')
  const rootRef = useRef<HTMLDivElement>(null)
  const queryClient = useQueryClient()
  const { shouldRender, isOpen } = useAnimatedUnmount(open, overlayExitMs('menu'))

  const { data } = useQuery({
    queryKey: ['cluster-sessions'],
    enabled: !!currentSlug,
    refetchInterval: open ? 2000 : 15000,
    retry: false,
    queryFn: async (): Promise<SessionsState | null> => {
      const resp = await fetch('/_sessions/api/state', { headers: { Accept: 'application/json' } })
      if (!resp.ok || !resp.headers.get('content-type')?.includes('application/json')) return null
      const body = (await resp.json()) as SessionsState
      return body.controller === CONTROLLER_NAME ? body : null
    },
  })

  const refresh = () => queryClient.invalidateQueries({ queryKey: ['cluster-sessions'] })
  const startSession = useMutation({
    mutationFn: (slug: string) => controllerPost(`sessions/${slug}/start`),
    onSettled: refresh,
    meta: { errorMessage: 'Failed to start the cluster session' },
  })
  const stopSession = useMutation({
    mutationFn: (slug: string) => controllerPost(`sessions/${slug}/stop`),
    onSettled: refresh,
    meta: { errorMessage: 'Failed to stop the cluster session' },
  })
  const stopOthers = useMutation({
    mutationFn: () => controllerPost('stop-all', { except: currentSlug }),
    onSettled: refresh,
    meta: { errorMessage: 'Failed to stop the other sessions' },
  })

  useEffect(() => {
    if (!open) return
    const onDown = (e: globalThis.MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown, true)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown, true)
      document.removeEventListener('keydown', onKey)
    }
  }, [open])

  if (!currentSlug || !data) return null

  const sessions = data.sessions
  const running = sessions.filter(s => s.state === 'running' || s.state === 'starting')
  const othersRunning = running.filter(s => s.slug !== currentSlug).length
  const q = filter.trim().toLowerCase()
  const shown = q ? sessions.filter(s => s.context.toLowerCase().includes(q)) : sessions
  const ordered = [...shown].sort((a, b) => {
    const rank = (s: ClusterSession) => (s.slug === currentSlug ? 0 : s.state === 'stopped' ? 2 : 1)
    return rank(a) - rank(b) || a.context.localeCompare(b.context)
  })

  // A different cluster is a different Radar under another base path, so
  // moving there is a full page load, never an in-app route change.
  const openSession = async (s: ClusterSession, newTab: boolean) => {
    if (s.state === 'stopped' || s.state === 'failed') {
      try {
        await startSession.mutateAsync(s.slug)
      } catch {
        return
      }
    }
    if (newTab) {
      window.open(sessionPath(s.slug), '_blank', 'noopener')
    } else {
      setOpen(false)
      window.location.assign(sessionPath(s.slug))
    }
  }

  const onRowClick = (e: MouseEvent, s: ClusterSession) => {
    if (s.slug === currentSlug) return
    void openSession(s, e.metaKey || e.ctrlKey)
  }

  const stop = (s: ClusterSession) => {
    if (s.slug === currentSlug && !window.confirm(`Stop ${s.context}? This tab will disconnect from it.`)) return
    stopSession.mutate(s.slug)
  }

  return (
    <div className="relative" ref={rootRef}>
      <Tooltip content="Cluster sessions: each tab can stay on its own cluster" position="bottom" disabled={open}>
        <button
          type="button"
          onClick={() => setOpen(!open)}
          aria-haspopup="menu"
          aria-expanded={open}
          aria-label="Cluster sessions"
          className="flex items-center gap-1.5 p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors text-xs font-medium"
        >
          <Layers className="w-4 h-4" />
          <span className="tabular-nums">{running.length}</span>
        </button>
      </Tooltip>
      {shouldRender && (
        <div
          role="menu"
          inert={!open || undefined}
          className={clsx(
            'absolute top-full right-0 mt-1 z-50 w-[380px] max-w-[calc(100vw-2rem)] bg-theme-surface border border-theme-border rounded-lg shadow-theme-lg origin-top-right',
            TRANSITION_MENU,
            isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-[0.97]',
            !open && 'pointer-events-none',
          )}
          style={overlayTransitionStyle(isOpen, 'menu')}
        >
          <div className="px-3 pt-3 pb-2 border-b-subtle">
            <div className="text-sm font-medium text-theme-text-primary">Cluster sessions</div>
            <div className="text-xs text-theme-text-tertiary">
              Each cluster runs in its own Radar. Click to move this tab, {'⌘'}/Ctrl-click for a new tab.
            </div>
            {sessions.length > 8 && (
              <input
                type="search"
                value={filter}
                onChange={e => setFilter(e.target.value)}
                placeholder="Filter clusters"
                aria-label="Filter clusters"
                className="mt-2 w-full px-2 py-1 text-xs rounded-md bg-theme-elevated border border-theme-border text-theme-text-primary placeholder:text-theme-text-tertiary"
              />
            )}
          </div>
          <div className="max-h-[360px] overflow-y-auto py-1">
            {ordered.length === 0 && <div className="px-3 py-2 text-xs text-theme-text-tertiary">No clusters match.</div>}
            {ordered.map(s => {
              const isCurrent = s.slug === currentSlug
              const pending = s.state === 'starting' || s.state === 'stopping'
              const canStop = s.state === 'running' || s.state === 'starting'
              return (
                <div
                  key={s.slug}
                  role="menuitem"
                  tabIndex={isCurrent ? -1 : 0}
                  onClick={e => onRowClick(e, s)}
                  onAuxClick={e => {
                    if (e.button === 1 && !isCurrent) {
                      e.preventDefault()
                      void openSession(s, true)
                    }
                  }}
                  onKeyDown={e => {
                    if ((e.key === 'Enter' || e.key === ' ') && !isCurrent) {
                      e.preventDefault()
                      void openSession(s, e.metaKey || e.ctrlKey)
                    }
                  }}
                  className={clsx(
                    'group flex items-center gap-2 px-3 py-1.5 text-xs',
                    isCurrent ? 'bg-theme-hover cursor-default' : 'cursor-pointer hover:bg-theme-hover',
                  )}
                >
                  {pending ? (
                    <Loader2 className="w-3 h-3 animate-spin text-theme-text-tertiary shrink-0" />
                  ) : (
                    <StatusDot tone={STATE_TONE[s.state]} size="md" className="shrink-0" />
                  )}
                  <span className="flex-1 min-w-0">
                    <Tooltip content={s.error ? `${s.context}: ${s.error}` : s.context} position="left">
                      <span className="block truncate text-theme-text-primary">{s.context}</span>
                    </Tooltip>
                  </span>
                  {isCurrent && <span className="shrink-0 text-accent">this tab</span>}
                  {s.state === 'failed' && <span className="shrink-0 text-theme-text-tertiary">failed</span>}
                  {s.rssBytes ? (
                    <span className="shrink-0 tabular-nums text-theme-text-tertiary">{formatMemory(s.rssBytes)}</span>
                  ) : null}
                  {!isCurrent && (
                    <Tooltip content="Open in a new tab" position="left">
                      <button
                        type="button"
                        aria-label={`Open ${s.context} in a new tab`}
                        onClick={e => {
                          e.stopPropagation()
                          void openSession(s, true)
                        }}
                        className="shrink-0 p-0.5 rounded text-theme-text-tertiary hover:text-theme-text-primary opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
                      >
                        <ExternalLink className="w-3 h-3" />
                      </button>
                    </Tooltip>
                  )}
                  {canStop && (
                    <Tooltip content="Stop this session and free its memory" position="left">
                      <button
                        type="button"
                        aria-label={`Stop ${s.context}`}
                        disabled={stopSession.isPending && stopSession.variables === s.slug}
                        onClick={e => {
                          e.stopPropagation()
                          stop(s)
                        }}
                        className="shrink-0 p-0.5 rounded text-theme-text-tertiary hover:text-theme-text-primary disabled:opacity-50"
                      >
                        <Square className="w-3 h-3" />
                      </button>
                    </Tooltip>
                  )}
                </div>
              )
            })}
          </div>
          <div className="flex items-center justify-between gap-2 px-3 py-2 border-t-subtle text-xs">
            <a href="/_sessions/" target="_blank" rel="noopener" className="text-theme-text-secondary hover:text-theme-text-primary">
              Manage sessions
            </a>
            <button
              type="button"
              disabled={othersRunning === 0 || stopOthers.isPending}
              onClick={() => stopOthers.mutate()}
              className="px-2 py-1 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary disabled:opacity-50 disabled:cursor-not-allowed"
            >
              Stop other sessions{othersRunning ? ` (${othersRunning})` : ''}
            </button>
          </div>
        </div>
      )}
    </div>
  )
}
