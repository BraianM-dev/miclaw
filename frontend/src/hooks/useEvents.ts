import { useEffect, useRef } from 'react'
import { connectEvents } from '../api/client'
import type { SSEEvent } from '../types'

/**
 * useEvents — subscribes to the gateway SSE stream and calls onEvent for each message.
 * Automatically reconnects on connection loss (handled inside connectEvents).
 * The handler ref pattern ensures the latest callback is always used without
 * re-creating the EventSource on every render.
 */
export function useEvents(onEvent: (event: SSEEvent) => void) {
  const handlerRef = useRef(onEvent)
  handlerRef.current = onEvent

  useEffect(() => {
    const disconnect = connectEvents((e) => handlerRef.current(e))
    return disconnect
  }, []) // mount / unmount only
}
