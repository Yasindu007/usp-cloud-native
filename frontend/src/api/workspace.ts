import { apiClient, unwrapEnvelope } from "@/api/client";
import type {
  ApiKeySummary,
  CreateApiKeyPayload,
  CreateApiKeyResponse,
  Workspace,
  WorkspaceMember,
} from "@/types/domain";

export function getWorkspace(workspaceId: string) {
  return unwrapEnvelope<Workspace>(apiClient.get(`/api/v1/workspaces/${workspaceId}`));
}

export function listMembers(workspaceId: string) {
  return unwrapEnvelope<WorkspaceMember[]>(apiClient.get(`/api/v1/workspaces/${workspaceId}/members`));
}

export function listApiKeys(workspaceId: string) {
  return unwrapEnvelope<ApiKeySummary[]>(apiClient.get(`/api/v1/workspaces/${workspaceId}/api-keys`));
}

export function createApiKey(workspaceId: string, payload: CreateApiKeyPayload) {
  return unwrapEnvelope<CreateApiKeyResponse>(
    apiClient.post(`/api/v1/workspaces/${workspaceId}/api-keys`, payload),
  );
}
