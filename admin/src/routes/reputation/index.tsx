import { createFileRoute } from "@tanstack/react-router";
import { useAuthStore } from "../../lib/stores/authStore";
import { AppShell } from "../../components/layout/AppShell";
import { ReputationTable } from "../../components/reputation/ReputationTable";

export const Route = createFileRoute("/reputation/")({
  component: ReputationPage,
});

// The operator's reputation view (#1590). The API answers it only to sessions
// holding queue:read at the server's own scope.
function ReputationPage() {
  const { accessToken } = useAuthStore();
  return (
    <AppShell title="Reputation">
      <p className="text-sm mb-6" style={{ color: "var(--gray-secondary)" }}>
        Which domain is hurting the server's sending reputation: bounces, DMARC results and complaints per domain,
        worst bounce rate first, with spikes against the window before flagged.
      </p>
      {accessToken && <ReputationTable accessToken={accessToken} />}
    </AppShell>
  );
}
