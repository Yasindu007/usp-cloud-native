import { apiClient, unwrapEnvelope } from "@/api/client";
import type { AnalyticsBreakdown, AnalyticsSummary, AnalyticsTimeSeries } from "@/types/domain";

export function getAnalyticsSummary(workspaceId: string, urlId: string, window: "24h" | "7d" | "30d") {
  return unwrapEnvelope<AnalyticsSummary>(
    apiClient.get(`/api/v1/workspaces/${workspaceId}/urls/${urlId}/analytics`, {
      params: { window },
    }),
  );
}

export function getAnalyticsTimeSeries(
  workspaceId: string,
  urlId: string,
  window: "24h" | "7d" | "30d",
  granularity: "1h" | "1d",
) {
  return unwrapEnvelope<AnalyticsTimeSeries>(
    apiClient.get(`/api/v1/workspaces/${workspaceId}/urls/${urlId}/analytics/timeseries`, {
      params: { window, granularity },
    }),
  );
}

export function getAnalyticsBreakdown(
  workspaceId: string,
  urlId: string,
  dimension: "country" | "device_type" | "referrer_domain",
  window: "24h" | "7d" | "30d",
) {
  return unwrapEnvelope<AnalyticsBreakdown>(
    apiClient.get(`/api/v1/workspaces/${workspaceId}/urls/${urlId}/analytics/breakdown`, {
      params: { dimension, window },
    }),
  );
}
