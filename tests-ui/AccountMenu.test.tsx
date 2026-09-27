import { fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { describe, expect, it, beforeEach } from 'vitest'
import AccountMenu from '@/components/auth/AccountMenu'
import { useServer } from '@/store/server'
import { useWorkspace } from '@/store/workspace'
import type { User } from '@/lib/api'

const user: User = {
  id: 'u1',
  username: 'alexandrosst',
  mustChangePassword: false,
  createdAt: new Date().toISOString(),
  twoFactorEnabled: true, // avoids the two-factor nudge banner, which is unrelated to this test
  emailVerified: false,
  emailOtpEnabled: false,
  mailConfigured: false,
  canManageMail: false,
  passkeys: [],
}

function seedConnected() {
  useServer.setState({
    status: 'connected',
    user,
    orgs: [{ id: 'org1', name: 'Acme', role: 'admin' }],
    orgId: 'org1',
    role: 'admin',
    registration: 'invite',
    checked: true,
    pendingMethods: [],
  } as never)
  useWorkspace.setState({ status: 'off' } as never)
}

// The sidebar this menu lives in always carries a CSS transform (for its mobile slide-in), which makes it
// the containing block for any `position: fixed` descendant - reproduced here because that's exactly the
// layout condition that broke the old full-screen overlay in production, even though it isn't what makes
// this test pass or fail (the fix no longer relies on any element's position at all).
function TransformedSidebar({ children }: { children: React.ReactNode }) {
  return <div style={{ transform: 'translateX(0)' }}>{children}</div>
}

describe('AccountMenu', () => {
  beforeEach(() => {
    seedConnected()
  })

  it('opens on click and closes when clicking elsewhere on the page, even inside a transformed ancestor', () => {
    render(
      <MemoryRouter>
        <TransformedSidebar>
          <AccountMenu />
        </TransformedSidebar>
        <button>Somewhere else entirely on the page</button>
      </MemoryRouter>,
    )

    fireEvent.click(screen.getByTestId('account').querySelector('button')!)
    expect(screen.getByTestId('account-menu')).toBeInTheDocument()

    // A real outside click is a mousedown followed by a click; the close logic runs on mousedown.
    fireEvent.mouseDown(screen.getByText('Somewhere else entirely on the page'))
    expect(screen.queryByTestId('account-menu')).not.toBeInTheDocument()
  })

  it('does not close when clicking inside the menu itself', () => {
    render(
      <MemoryRouter>
        <TransformedSidebar>
          <AccountMenu />
        </TransformedSidebar>
      </MemoryRouter>,
    )

    fireEvent.click(screen.getByTestId('account').querySelector('button')!)
    const menu = screen.getByTestId('account-menu')
    fireEvent.mouseDown(menu)
    expect(screen.getByTestId('account-menu')).toBeInTheDocument()
  })

  it('closes on Escape', () => {
    render(
      <MemoryRouter>
        <AccountMenu />
      </MemoryRouter>,
    )

    fireEvent.click(screen.getByTestId('account').querySelector('button')!)
    expect(screen.getByTestId('account-menu')).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'Escape' })
    expect(screen.queryByTestId('account-menu')).not.toBeInTheDocument()
  })
})
