export function formatNumber(value: number | null | undefined) {
  return new Intl.NumberFormat("en-US").format(value ?? 0);
}

export function formatDateTime(value: string | number | Date | null | undefined) {
  if (!value) {
    return "N/A";
  }

  return new Intl.DateTimeFormat("en-US", {
    dateStyle: "medium",
    timeStyle: "short",
  }).format(new Date(value));
}

export function formatRelativeDays(window: "24h" | "7d" | "30d") {
  if (window === "24h") return "Last 24 hours";
  if (window === "7d") return "Last 7 days";
  return "Last 30 days";
}

export function truncateMiddle(value: string, keep = 8) {
  if (value.length <= keep * 2) {
    return value;
  }

  return `${value.slice(0, keep)}...${value.slice(-keep)}`;
}

export function ensureErrorMessage(error: unknown) {
  if (error instanceof Error) {
    return error.message;
  }

  return "An unexpected error occurred.";
}
