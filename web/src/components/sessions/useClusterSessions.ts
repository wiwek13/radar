import { useMemo } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { getBasename } from '../../api/config'
import { readErrorBody } from '../../api/httpErrors'

export type SessionState = 'stopped' | 'starting' | 'running' | 'stopping' | 'failed'

export interface ClusterSession {
  context: string
  slug: string
  state: SessionState
  rssBytes?: number
  error?: string
}

// The session controller (cmd/radar-sessions) serves each cluster's Radar
// under /s/<slug>/ and answers its own API at the origin root. These calls go
// to the controller, not to this Radar, so they deliberately bypass
// getApiBase(). Outside the controller the basename never matches and no
// request is made.
const SESSION_BASENAME = /^\/s\/([a-z0-9]+(?:-[a-z0-9]+)*)$/
const CONTROLLER_NAME = 'radar-sessions'
const QUERY_KEY = ['cluster-sessions']

// Loading this path starts the session if it is not running.
export const sessionPath = (slug: string) => `/s/${slug}/`

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

export interface ClusterSessions {
  currentSlug: string
  sessions: ClusterSession[]
  stop: (slug: string) => Promise<unknown>
  stopOthers: () => void
  stoppingSlug?: string
  stoppingOthers: boolean
}

/** The session controller's view of every cluster, or null when this Radar
 *  is not running behind it. */
export function useClusterSessions(): ClusterSessions | null {
  const currentSlug = useMemo(() => SESSION_BASENAME.exec(getBasename())?.[1], [])
  const queryClient = useQueryClient()

  const { data } = useQuery({
    queryKey: QUERY_KEY,
    enabled: !!currentSlug,
    refetchInterval: 3000,
    retry: false,
    queryFn: async (): Promise<ClusterSession[] | null> => {
      const resp = await fetch('/_sessions/api/state', { headers: { Accept: 'application/json' } })
      if (!resp.ok || !resp.headers.get('content-type')?.includes('application/json')) return null
      const body = (await resp.json()) as { controller?: string; sessions?: ClusterSession[] }
      return body.controller === CONTROLLER_NAME ? body.sessions ?? [] : null
    },
  })

  const refresh = () => queryClient.invalidateQueries({ queryKey: QUERY_KEY })
  const stop = useMutation({
    mutationFn: (slug: string) => controllerPost(`sessions/${slug}/stop`),
    onSettled: refresh,
    meta: { errorMessage: 'Failed to stop the cluster session' },
  })
  const stopOthers = useMutation({
    mutationFn: () => controllerPost('stop-all', { except: currentSlug }),
    onSettled: refresh,
    meta: { errorMessage: 'Failed to stop the other sessions' },
  })

  if (!currentSlug || !data) return null
  return {
    currentSlug,
    sessions: data,
    stop: stop.mutateAsync,
    stopOthers: () => stopOthers.mutate(),
    stoppingSlug: stop.isPending ? stop.variables : undefined,
    stoppingOthers: stopOthers.isPending,
  }
}
