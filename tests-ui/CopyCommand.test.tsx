import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { CopyCommand } from '@/components/agents/AgentInsight'

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
})
