import { Component, type ErrorInfo, type ReactNode } from 'react'

/**
 * Keeps one broken feature from taking the whole page down. A route (or a section of one) that throws while
 * rendering shows this instead of a blank screen; the rest of the app is unaffected, and switching pages recovers.
 */
export default class ErrorBoundary extends Component<{ children: ReactNode; label?: string }, { error?: Error }> {
  state: { error?: Error } = {}
  static getDerivedStateFromError(error: Error) {
    return { error }
  }
  componentDidCatch(error: Error, info: ErrorInfo) {
    // eslint-disable-next-line no-console
    console.error(`[${this.props.label ?? 'page'}] crashed while rendering:`, error, info.componentStack)
  }
  render() {
    if (this.state.error) {
      return (
        <div className="flex h-full min-h-40 flex-col items-center justify-center gap-2 p-8 text-center" role="alert">
          <p className="text-sm font-medium text-nb-300">Something went wrong showing this page.</p>
          <p className="max-w-md text-sm text-nb-500">{this.state.error.message || 'An unexpected error occurred.'} Switching pages usually recovers; if it keeps happening, tell an administrator.</p>
        </div>
      )
    }
    return this.props.children
  }
}
