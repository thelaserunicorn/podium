import { Component, type ErrorInfo, type ReactNode } from "react";

// ErrorBoundary catches render-time exceptions in its subtree and
// renders a recoverable "Something went wrong" panel instead of
// blanking the entire page. Without this, an exception in any tab or
// child component unmounts the whole route tree (React 18 default
// behaviour), which is what we saw on the Logs tab when the user's
// app was deleted mid-render.
//
// We keep the boundary intentionally thin: no retry, no error
// reporting hook — just enough to give the user a way back to the
// app list. The browser console still has the full stack for
// debugging.
export class ErrorBoundary extends Component<{ children: ReactNode }, { error: Error | null }> {
  state = { error: null as Error | null };

  static getDerivedStateFromError(error: Error) {
    return { error };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    // eslint-disable-next-line no-console
    console.error("ErrorBoundary caught", error, info.componentStack);
  }

  reset = () => {
    this.setState({ error: null });
  };

  render() {
    if (this.state.error) {
      return (
        <div className="space-y-3 p-4">
          <h2 className="text-lg font-semibold">Something went wrong</h2>
          <p className="text-sm text-muted-foreground">
            {this.state.error.message || "An unexpected error occurred while rendering this page."}
          </p>
          <div className="flex gap-2">
            <button
              type="button"
              onClick={this.reset}
              className="rounded-md border border-border bg-background px-3 py-1 text-xs hover:bg-muted"
            >
              Try again
            </button>
            <a
              href="/apps"
              className="rounded-md border border-border bg-background px-3 py-1 text-xs hover:bg-muted"
            >
              Back to apps
            </a>
          </div>
        </div>
      );
    }
    return this.props.children;
  }
}
