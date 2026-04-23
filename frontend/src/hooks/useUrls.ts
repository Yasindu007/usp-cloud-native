import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import toast from "react-hot-toast";
import { createUrl, listUrls } from "@/api/urls";
import { ensureErrorMessage } from "@/utils/format";
import { useSessionStore } from "@/hooks/useSession";
import type { ShortenRequest } from "@/types/domain";

export function useUrlList() {
  const workspaceId = useSessionStore((state) => state.workspaceId);

  return useQuery({
    queryKey: ["urls", workspaceId],
    queryFn: () => listUrls(workspaceId),
    enabled: Boolean(workspaceId),
  });
}

export function useCreateUrl() {
  const workspaceId = useSessionStore((state) => state.workspaceId);
  const queryClient = useQueryClient();

  return useMutation({
    mutationFn: (payload: ShortenRequest) => createUrl(workspaceId, payload),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["urls", workspaceId] });
      toast.success("Short URL created.");
    },
    onError: (error) => {
      toast.error(ensureErrorMessage(error));
    },
  });
}
