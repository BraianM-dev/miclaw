import { useEffect, useRef } from 'react'
import { connectEvents } from '../api/client'
import type { SSEEvent } from '../types'

// useEvents suscribe al stream SSE del gateway y llama onEvent por cada mensaje.
// Se reconecta automáticamente si la conexión se pierde (manejado por EventSource).
export function useEvents(onEvent: (event: SSEEvent) => void) {
  const cbRef = useRef(onEvent)
  cbRef.current = onEvent

  useEffect(() => {
    const disconnect = connectEvents((raw) => {
      cbRef.current(raw as SSEEvent)
    })
    return disconnect
  }, []) // solo montar/desmontar una vez
}
