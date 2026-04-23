import { apiClient, unwrapEnvelope } from "@/api/client";
import type { ListUrlsResponse, ShortenRequest, ShortenResponse, UrlRecord } from "@/types/domain";

export function createUrl(workspaceId: string, payload: ShortenRequest) {
  return unwrapEnvelope<ShortenResponse>(
    apiClient.post(`/api/v1/workspaces/${workspaceId}/urls`, payload),
  );
}

export async function listUrls(workspaceId: string, limit = 25) {
  const response = await apiClient.get<ListUrlsResponse>(`/api/v1/workspaces/${workspaceId}/urls`, {
    params: { limit },
  });

  return response.data;
}

export function getUrl(workspaceId: string, urlId: string) {
  return unwrapEnvelope<UrlRecord>(apiClient.get(`/api/v1/workspaces/${workspaceId}/urls/${urlId}`));
}
