/**
 * The message an API error carries, whatever shape it arrived in: the
 * management API answers {"error": ..., "message": ...}; older handlers
 * nest it as {"error": {"message": ...}}; a network failure has only the
 * Error's own message.
 */
export function apiErrorMessage(err: unknown, fallback: string): string {
  const ax = err as { response?: { data?: { message?: string; error?: string | { message?: string } } } }
  const data = ax?.response?.data
  if (data) {
    if (typeof data.message === 'string' && data.message) return data.message
    if (typeof data.error === 'object' && data.error?.message) return data.error.message
  }
  return err instanceof Error && err.message ? err.message : fallback
}
