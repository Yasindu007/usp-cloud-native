import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import toast from "react-hot-toast";
import { createApiKey, getWorkspace, listApiKeys, listMembers } from "@/api/workspace";
import { useSessionStore } from "@/hooks/useSession";
import { ensureErrorMessage } from "@/utils/format";
import type { CreateApiKeyPayload } from "@/types/domain";

export function useWorkspaceDetails() {
  const workspaceId = useSessionStore((state) => state.workspaceId);

  return useQuery({
    queryKey: ["workspace", workspaceId],
    queryFn: () => getWorkspace(workspaceId),
    enabled: Boolean(workspaceId),
  });
}

export function useWorkspaceMembers() {
  const workspaceId = useSessionStore((state) => state.workspaceId);

  return useQuery({
    queryKey: ["workspace-members", workspaceId],
    queryFn: () => listMembers(workspaceId),
    enabled: Boolean(workspaceId),
  });
}

export function useApiKeys() {
  const workspaceId = useSessionStore((state) => state.workspaceId);

  return useQuery({
    queryKey: ["workspace-api-keys", workspaceId],
    queryFn: () => listApiKeys(workspaceId),
    enabled: Boolean(workspaceId),
  });
}

export function useCreateApiKey() {
  const workspaceId = useSessionStore((state) => state.workspaceId);
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (payload: CreateApiKeyPayload) => createApiKey(workspaceId, payload),
    onSuccess: (data) => {
      void queryClient.invalidateQueries({ queryKey: ["workspace-api-keys", workspaceId] });
      toast.success(`API key ${data.name} created. Copy it now.`);
    },
    onError: (error) => {
      toast.error(ensureErrorMessage(error));
    },
  });
}
