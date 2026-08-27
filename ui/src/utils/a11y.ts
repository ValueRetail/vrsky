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

// treeNodeKeyDown returns an onKeyDown handler for an expandable role="treeitem":
// Enter/Space and the expand/collapse arrows all drive the item's own click
// handler (which owns the toggle logic) — ArrowRight only when collapsed,
// ArrowLeft only when expanded — so there's no toggle logic to duplicate.
export function treeNodeKeyDown(isExpanded: boolean) {
  return (e: KeyboardEvent<HTMLElement>) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault()
      e.currentTarget.click()
    } else if (e.key === 'ArrowRight' && !isExpanded) {
      e.preventDefault()
      e.currentTarget.click()
    } else if (e.key === 'ArrowLeft' && isExpanded) {
      e.preventDefault()
      e.currentTarget.click()
    }
  }
}
