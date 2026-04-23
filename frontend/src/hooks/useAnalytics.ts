import { useQueries, useQuery } from "@tanstack/react-query";
import {
  getAnalyticsBreakdown,
  getAnalyticsSummary,
  getAnalyticsTimeSeries,
} from "@/api/analytics";
import { useSessionStore } from "@/hooks/useSession";

export function useSummaryWindows(urlId: string) {
  const workspaceId = useSessionStore((state) => state.workspaceId);

  return useQueries({
    queries: (["24h", "7d", "30d"] as const).map((window) => ({
      queryKey: ["analytics-summary", workspaceId, urlId, window],
      queryFn: () => getAnalyticsSummary(workspaceId, urlId, window),
      enabled: Boolean(workspaceId && urlId),
    })),
  });
}

export function useTimeSeries(urlId: string) {
  const workspaceId = useSessionStore((state) => state.workspaceId);

  return useQuery({
    queryKey: ["analytics-timeseries", workspaceId, urlId],
    queryFn: () => getAnalyticsTimeSeries(workspaceId, urlId, "30d", "1d"),
    enabled: Boolean(workspaceId && urlId),
  });
}

export function useBreakdown(urlId: string, dimension: "country" | "device_type" | "referrer_domain") {
  const workspaceId = useSessionStore((state) => state.workspaceId);

  return useQuery({
    queryKey: ["analytics-breakdown", workspaceId, urlId, dimension],
    queryFn: () => getAnalyticsBreakdown(workspaceId, urlId, dimension, "30d"),
    enabled: Boolean(workspaceId && urlId),
  });
}
