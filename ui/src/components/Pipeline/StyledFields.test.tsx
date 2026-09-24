import { describe, it, expect, vi } from 'vitest'
import { render, screen, fireEvent } from '@testing-library/react'
import { StyledInput, StyledSelect } from './StyledFields'

describe('StyledFields', () => {
  it('StyledInput labels its input, reports changes, and marks focus', () => {
    const onChange = vi.fn()
    render(<StyledInput label="Name" value="" onChange={onChange} placeholder="x" />)
    const input = screen.getByLabelText('Name')
    fireEvent.focus(input)
    expect(input.style.borderColor || input.style.border).toContain('59, 130, 246')
    fireEvent.change(input, { target: { value: 'abc' } })
    expect(onChange).toHaveBeenCalledWith('abc')
    fireEvent.blur(input)
    expect(input.style.border).not.toContain('59, 130, 246')
  })

  it('StyledSelect labels its select, lists options, and reports changes', () => {
    const onChange = vi.fn()
    render(<StyledSelect label="Kind" value="a" onChange={onChange}
      options={[{ value: 'a', label: 'A' }, { value: 'b', label: 'B' }]} />)
    const select = screen.getByLabelText('Kind') as HTMLSelectElement
    fireEvent.focus(select)
    fireEvent.change(select, { target: { value: 'b' } })
    fireEvent.blur(select)
    expect(onChange).toHaveBeenCalledWith('b')
    expect(Array.from(select.options).map((o) => o.textContent)).toEqual(['A', 'B'])
  })
})
