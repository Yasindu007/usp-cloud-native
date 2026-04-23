import { EventSourcePolyfill } from "event-source-polyfill";
import { env } from "@/utils/env";

export function openWorkspaceStream(workspaceId: string, token: string) {
  return new EventSourcePolyfill(`${env.apiUrl}/api/v1/workspaces/${workspaceId}/stream`, {
    headers: {
      Authorization: `Bearer ${token}`,
    },
  });
}
