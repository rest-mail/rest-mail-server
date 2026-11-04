import { Lock } from "lucide-react";

// AccessDenied is what a screen shows in place of itself when the session does
// not hold what the screen's API route requires.
export function AccessDenied() {
  return (
    <div className="flex items-center justify-center h-96" role="status">
      <div className="text-center max-w-md">
        <Lock
          className="w-12 h-12 mx-auto mb-4"
          style={{ color: "var(--gray-secondary)" }}
        />
        <p
          style={{ color: "var(--black-soft)" }}
          className="font-semibold mb-2"
        >
          You don't have access to this
        </p>
        <p style={{ color: "var(--gray-secondary)" }}>
          Your account can't use this screen. If you need it, ask whoever runs
          the server.
        </p>
      </div>
    </div>
  );
}
