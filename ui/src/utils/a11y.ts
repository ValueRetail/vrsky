import type { KeyboardEvent } from 'react'

// onKeyActivate makes a non-button clickable element keyboard-operable: Enter or
// Space triggers the element's own click handler (via the native click()), so
// there's no logic to duplicate. Pair it with role="button" + tabIndex={0}.
//
//   <div role="button" tabIndex={0} onClick={fn} onKeyDown={onKeyActivate}>
export function onKeyActivate(e: KeyboardEvent<HTMLElement>) {
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    e.currentTarget.click()
  }
}
