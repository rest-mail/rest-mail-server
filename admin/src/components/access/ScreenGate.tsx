import type { ReactNode } from "react";
import { useRouterState } from "@tanstack/react-router";
import { useAuthStore } from "../../lib/stores/authStore";
import { canReach } from "../../lib/access";
import { AppShell } from "../layout/AppShell";
import { AccessDenied } from "./AccessDenied";

// ScreenGate renders the current screen only when the session holds what the
// screen's API route requires. Otherwise the screen is not mounted at all (so
// it fetches nothing) and the access message shows in its place.
export function ScreenGate({ children }: { children: ReactNode }) {
  const pathname = useRouterState({ select: (s) => s.location.pathname });
  const { user, isAuthenticated } = useAuthStore();

  if (isAuthenticated && !canReach(user, pathname)) {
    return (
      <AppShell title="No access">
        <AccessDenied />
      </AppShell>
    );
  }
  return <>{children}</>;
}
