import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, beforeEach, describe, expect, test, vi } from 'vitest'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { CopyButton, CopyIconButton, CopyValue } from '@/components/ui/primitives'
import { copyToClipboard } from '@/lib/clipboard'

// A server reached over plain http has no navigator.clipboard at all. A copy button that then did nothing looked like it worked, and for a token
// shown once that is a lost token: it must fall back, and when nothing works, say so and leave the text selected.

const original = Object.getOwnPropertyDescriptor(navigator, 'clipboard')
const setClipboard = (value: unknown) => Object.defineProperty(navigator, 'clipboard', { value, configurable: true })
// jsdom has no document.execCommand: stood in for here, as the browsers' older copy route.
const execCommand = vi.fn<(cmd: string) => boolean>()

beforeEach(() => {
  execCommand.mockReset()
  ;(document as unknown as { execCommand: typeof execCommand }).execCommand = execCommand
})
afterEach(() => {
  if (original) Object.defineProperty(navigator, 'clipboard', original)
  else delete (navigator as unknown as { clipboard?: unknown }).clipboard
  delete (document as unknown as { execCommand?: unknown }).execCommand
})

describe('copyToClipboard', () => {
  test('uses the Clipboard API when there is one', async () => {
    const writeText = vi.fn(async () => undefined)
    setClipboard({ writeText })
    expect(await copyToClipboard('abc')).toBe('copied')
    expect(writeText).toHaveBeenCalledWith('abc')
    expect(execCommand).not.toHaveBeenCalled()
  })

  test('with navigator.clipboard undefined, falls back to a hidden text area and execCommand, and cleans it up', async () => {
    setClipboard(undefined)
    let copiedText = ''
    execCommand.mockImplementation(() => {
      copiedText = document.querySelector('textarea')?.value ?? '' // the hidden text area holds the text
      return true
    })
    expect(await copyToClipboard('the token')).toBe('copied')
    expect(execCommand).toHaveBeenCalledWith('copy')
    expect(copiedText).toBe('the token')
    expect(document.querySelector('textarea')).toBeNull()
  })

  test('a rejected writeText (an unfocused page) is not an unhandled rejection: it falls back too', async () => {
    setClipboard({ writeText: vi.fn(async () => { throw new DOMException('denied', 'NotAllowedError') }) })
    execCommand.mockReturnValue(true)
    expect(await copyToClipboard('abc')).toBe('copied')
  })

  test('when nothing works it says "manual" and leaves the text on the page selected', async () => {
    setClipboard(undefined)
    execCommand.mockReturnValue(false)
    const el = document.createElement('code')
    el.textContent = 'select me'
    document.body.appendChild(el)
    expect(await copyToClipboard('select me', el)).toBe('manual')
    expect(window.getSelection()?.toString()).toBe('select me')
    el.remove()
  })
})

// userEvent.setup() installs a clipboard of its own on navigator, so the page's "no clipboard" is set after it.
const noClipboardUser = () => {
  const user = userEvent.setup()
  setClipboard(undefined)
  return user
}

describe('the copy buttons with no clipboard', () => {
  beforeEach(() => {
    execCommand.mockReturnValue(false)
  })

  test('CopyCommand leaves the command selected and says to press Ctrl+C', async () => {
    render(<CopyCommand text="helm upgrade x" testId="cmd" />)
    await noClipboardUser().click(screen.getByRole('button', { name: 'Copy the command' }))
    expect(await screen.findByTestId('copy-manual-hint')).toHaveTextContent('Press Ctrl+C to copy')
    expect(window.getSelection()?.toString()).toBe('helm upgrade x')
  })

  test('CopyButton does not claim it copied: it says so and offers the text, selected', async () => {
    render(<CopyButton text="the text" />)
    await noClipboardUser().click(screen.getByRole('button', { name: /Copy/ }))
    expect(screen.queryByText('Copied')).not.toBeInTheDocument()
    expect(await screen.findByRole('status')).toHaveTextContent('Press Ctrl+C to copy')
    const box = screen.getByLabelText('Text to copy') as HTMLTextAreaElement
    expect(box.value).toBe('the text')
    expect(box.selectionStart).toBe(0)
    expect(box.selectionEnd).toBe('the text'.length)
  })

  test('CopyIconButton says so too', async () => {
    render(<CopyIconButton text="10.0.0.0/8" title="Copy the CIDR" />)
    await noClipboardUser().click(screen.getByRole('button', { name: 'Copy the CIDR' }))
    expect(await screen.findByRole('status')).toHaveTextContent('Press Ctrl+C to copy')
    expect((screen.getByLabelText('Text to copy') as HTMLTextAreaElement).value).toBe('10.0.0.0/8')
  })

  test('CopyValue (a token shown once) leaves it selected and says so', async () => {
    render(<CopyValue value="cnf_SECRET" testId="tok" />)
    await noClipboardUser().click(screen.getByRole('button', { name: /Copy/ }))
    expect(await screen.findByTestId('tok-manual')).toHaveTextContent('Press Ctrl+C to copy')
    expect(window.getSelection()?.toString()).toBe('cnf_SECRET')
  })

  test('and when the fallback does work, they say Copied', async () => {
    execCommand.mockReturnValue(true)
    render(<CopyButton text="x" />)
    await noClipboardUser().click(screen.getByRole('button', { name: /Copy/ }))
    expect(await screen.findByText('Copied')).toBeInTheDocument()
  })
})
