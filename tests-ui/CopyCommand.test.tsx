import { fireEvent, render, screen } from '@testing-library/react'
import { describe, expect, test, vi } from 'vitest'
import { CopyCommand } from '@/components/agents/AgentInsight'
import { useServer } from '@/store/server'

describe('CopyCommand', () => {
  test('a command with line breaks of its own keeps them on screen, and a long one scrolls inside its box', () => {
    // The server's Secret commands are here-documents: run together on one line they cannot be read before they are pasted.
    render(<CopyCommand text={"kubectl apply -f - <<'EOF'\nkind: Secret\nEOF"} testId="multi" />)
    expect(screen.getByTestId('multi')).toHaveClass('whitespace-pre-wrap', 'overflow-y-auto')
  })

  test('a one-line command stays one line', () => {
    render(<CopyCommand text="helm list" testId="single" />)
    expect(screen.getByTestId('single')).not.toHaveClass('whitespace-pre-wrap')
  })

  test('copying a command to run in a cluster makes the state poll faster for a while', () => {
    const boostPolling = vi.fn(() => Promise.resolve())
    useServer.setState({ boostPolling })
    render(<CopyCommand text="helm upgrade agent --set flowObserver.enabled=true" />)
    fireEvent.click(screen.getByRole('button', { name: 'Copy the command' }))
    expect(boostPolling).toHaveBeenCalledTimes(1)
  })
})
