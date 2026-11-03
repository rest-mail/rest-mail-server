import type { ReactNode } from "react";

export function SectionHeading({ id, children }: { id: string; children: ReactNode }) {
  return (
    <h2
      id={id}
      className="text-lg font-semibold mb-4"
      style={{ fontFamily: "Space Grotesk", color: "var(--black-soft)" }}
    >
      {children}
    </h2>
  );
}

export function SectionError({
  message,
  onRetry,
}: {
  message: string;
  onRetry?: () => void;
}) {
  return (
    <div
      role="alert"
      className="mb-4 p-4 border flex items-center justify-between gap-3"
      style={{ borderColor: "#EF4444", backgroundColor: "#FEF2F2" }}
    >
      <span className="text-sm" style={{ color: "#B91C1C" }}>
        {message}
      </span>
      {onRetry && (
        <button
          type="button"
          onClick={onRetry}
          className="text-sm font-medium whitespace-nowrap"
          style={{ color: "#B91C1C" }}
        >
          Try again
        </button>
      )}
    </div>
  );
}
