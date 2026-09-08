import { useEffect, useState } from 'react'
import type { Download } from './types'

interface QueueItem {
  song: { title: string; artist: string; album: string }
}

interface DownloadState {
  downloads: Download[]
  queue: QueueItem[]
  active: number
  queued: number
}

const EMPTY: DownloadState = { downloads: [], queue: [], active: 0, queued: 0 }

let state: DownloadState = EMPTY
let listeners = new Set<() => void>()
let es: EventSource | null = null
let reconnectTimer: ReturnType<typeof setTimeout> | null = null
let attempts = 0

// The events endpoint requires auth, so connecting while logged out just loops
// on 401. App gates the stream on the authenticated flag instead.
let enabled = false

function backoffDelay() {
  // 1s, 2s, 4s ... capped at 30s, with jitter so reconnects do not sync up.
  const base = Math.min(1000 * 2 ** attempts, 30000)
  return base + Math.random() * 500
}

function connect() {
  if (es || !enabled) return

  es = new EventSource('/api/events')

  es.onopen = () => {
    attempts = 0
  }

  es.addEventListener('download-state', (e) => {
    try {
      state = JSON.parse(e.data)
      listeners.forEach(fn => fn())
    } catch { /* ignore malformed frame */ }
  })

  es.onerror = () => {
    es?.close()
    es = null
    if (!enabled || listeners.size === 0) return

    if (reconnectTimer) clearTimeout(reconnectTimer)
    reconnectTimer = setTimeout(connect, backoffDelay())
    attempts++
  }
}

function disconnect() {
  if (reconnectTimer) {
    clearTimeout(reconnectTimer)
    reconnectTimer = null
  }
  es?.close()
  es = null
  attempts = 0
}

/**
 * Enables or disables the download event stream. Call with false on logout so
 * the stream stops instead of retrying against a 401, and with true once
 * authenticated.
 */
export function setStreamEnabled(next: boolean) {
  if (enabled === next) return
  enabled = next

  if (!enabled) {
    disconnect()
    state = EMPTY
    listeners.forEach(fn => fn())
    return
  }
  if (listeners.size > 0) connect()
}

export function getDownloadState(): DownloadState {
  return state
}

export function useDownloadState(): DownloadState {
  const [s, setS] = useState<DownloadState>(state)

  useEffect(() => {
    const update = () => setS({ ...state })
    listeners.add(update)
    connect()
    return () => {
      listeners.delete(update)
      if (listeners.size === 0) disconnect()
    }
  }, [])

  return s
}
