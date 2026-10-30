import { forwardRef, useMemo } from 'react'
import { ExternalLink, Loader2, Square } from 'lucide-react'
import {
  ClusterSwitcher,
  parseContextName,
  type ClusterSwitcherItem,
  type StatusTone,
} from '@skyhook-io/k8s-ui'
import { Tooltip } from '../ui/Tooltip'
import { sessionPath, type ClusterSession, type ClusterSessions, type SessionState } from './useClusterSessions'

interface SessionSwitcherProps {
  sessions: ClusterSessions
  className?: string
  variant?: 'chip' | 'segment'
  label?: string
}

const STATE_TONE: Partial<Record<SessionState, StatusTone>> = {
  running: 'healthy',
  starting: 'degraded',
  stopping: 'degraded',
  failed: 'unhealthy',
}

const RUNNING_GROUP = { key: 'running', label: 'Running' }
const IDLE_GROUP = { key: 'idle', label: 'Not running' }

const isLive = (s: ClusterSession) => s.state === 'running' || s.state === 'starting'

function formatMemory(bytes?: number) {
  if (!bytes) return ''
  const mb = bytes / (1024 * 1024)
  return mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${Math.round(mb)} MB`
}

const iconButton =
  'p-1 rounded text-theme-text-tertiary hover:text-theme-text-primary hover:bg-theme-elevated ' +
  'focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--color-brand)] ' +
  'disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-transparent disabled:hover:text-theme-text-tertiary'

/**
 * The cluster switcher when this Radar runs behind radar-sessions: every
 * kubeconfig context is its own Radar process under /s/<slug>/. Picking a
 * cluster loads that Radar in this tab (the controller starts it if needed),
 * so other tabs keep their cluster.
 */
export const SessionSwitcher = forwardRef<{ open: () => void }, SessionSwitcherProps>(
  ({ sessions, className, variant, label }, ref) => {
    const { currentSlug, stop, stopOthers, stoppingSlug, stoppingOthers } = sessions
    const bySlug = useMemo(() => new Map(sessions.sessions.map(s => [s.slug, s])), [sessions.sessions])

    const items = useMemo<ClusterSwitcherItem[]>(() => {
      const rank = (s: ClusterSession) => (s.slug === currentSlug ? 0 : s.state === 'stopped' ? 2 : 1)
      return [...sessions.sessions]
        .sort((a, b) => rank(a) - rank(b) || a.context.localeCompare(b.context))
        .map(s => {
          const parsed = parseContextName(s.context)
          return {
            id: s.slug,
            name: s.context,
            secondary: parsed.provider ? s.context : undefined,
            badge: parsed.region || undefined,
            status: STATE_TONE[s.state],
            href: sessionPath(s.slug),
            group: s.state === 'stopped' ? IDLE_GROUP : RUNNING_GROUP,
          }
        })
    }, [sessions.sessions, currentSlug])

    const current = bySlug.get(currentSlug)
    const live = sessions.sessions.filter(isLive)
    const otherLive = live.filter(s => s.slug !== currentSlug)

    const stopSession = async (s: ClusterSession) => {
      if (s.slug !== currentSlug) {
        void stop(s.slug).catch(() => {})
        return
      }
      // This tab's Radar is going away, so the tab moves to another running
      // cluster rather than being left on a dead session.
      const next = otherLive[0]
      if (!next) return
      if (!window.confirm(`Stop ${s.context}? This tab moves to ${next.context}.`)) return
      try {
        await stop(s.slug)
      } catch {
        return
      }
      window.location.assign(sessionPath(next.slug))
    }

    const renderItemActions = (item: ClusterSwitcherItem, isCurrent: boolean) => {
      const s = bySlug.get(item.id)
      if (!s) return null
      const stopping = stoppingSlug === s.slug || s.state === 'stopping'
      const lastOne = isCurrent && otherLive.length === 0
      return (
        <>
          {s.rssBytes ? (
            <span className="px-1 text-[10px] tabular-nums text-theme-text-tertiary">{formatMemory(s.rssBytes)}</span>
          ) : null}
          {s.state === 'failed' && (
            <Tooltip content={s.error || 'The session exited'} position="left">
              <span className="px-1 text-[10px] text-theme-text-tertiary">failed</span>
            </Tooltip>
          )}
          {!isCurrent && (
            <Tooltip content="Open in a new tab" position="left">
              <button
                type="button"
                aria-label={`Open ${s.context} in a new tab`}
                onClick={() => window.open(sessionPath(s.slug), '_blank', 'noopener')}
                className={iconButton}
              >
                <ExternalLink className="w-3.5 h-3.5" />
              </button>
            </Tooltip>
          )}
          {(isLive(s) || stopping) && (
            <Tooltip
              content={
                lastOne
                  ? 'This is the only running session. Press Ctrl+C in the radar-fork terminal to stop it.'
                  : isCurrent
                    ? 'Stop this session and move this tab to another running cluster'
                    : 'Stop this session and free its memory'
              }
              position="left"
            >
              <button
                type="button"
                aria-label={`Stop ${s.context}`}
                disabled={stopping || lastOne}
                onClick={() => void stopSession(s)}
                className={iconButton}
              >
                {stopping ? <Loader2 className="w-3.5 h-3.5 animate-spin" /> : <Square className="w-3.5 h-3.5" />}
              </button>
            </Tooltip>
          )}
        </>
      )
    }

    return (
      <ClusterSwitcher
        ref={ref}
        className={className}
        variant={variant}
        label={label}
        currentId={currentSlug}
        currentName={current?.context ?? currentSlug}
        items={items}
        searchable={items.length > 1}
        showGroupHeaders
        showCurrentBullet={false}
        renderItemActions={renderItemActions}
        footerSlot={
          <div className="flex items-center justify-between gap-2 px-3 py-2 text-xs">
            <span className="text-theme-text-tertiary">
              {live.length} running. {'⌘'}/Ctrl-click opens a new tab.
            </span>
            <button
              type="button"
              disabled={otherLive.length === 0 || stoppingOthers}
              onClick={stopOthers}
              className="shrink-0 px-2 py-1 rounded text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-[var(--color-brand)] disabled:opacity-40 disabled:cursor-not-allowed disabled:hover:bg-transparent"
            >
              Stop others{otherLive.length ? ` (${otherLive.length})` : ''}
            </button>
          </div>
        }
      />
    )
  },
)
SessionSwitcher.displayName = 'SessionSwitcher'
