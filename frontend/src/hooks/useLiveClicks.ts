import { useEffect, useMemo, useState } from "react";
import { openWorkspaceStream } from "@/api/stream";
import { useSessionStore } from "@/hooks/useSession";
import type { ClickEvent } from "@/types/domain";

export function useLiveClicks() {
  const workspaceId = useSessionStore((state) => state.workspaceId);
  const token = useSessionStore((state) => state.token);
  const [events, setEvents] = useState<ClickEvent[]>([]);
  const [status, setStatus] = useState<"idle" | "connecting" | "open" | "error">("idle");

  useEffect(() => {
    if (!workspaceId || !token) {
      setStatus("idle");
      setEvents([]);
      return;
    }

    setStatus("connecting");
    const eventSource = openWorkspaceStream(workspaceId, token);

    eventSource.addEventListener("open", () => {
      setStatus("open");
    });

    eventSource.addEventListener("connected", () => {
      setStatus("open");
    });

    eventSource.addEventListener("click", (event: MessageEvent<string>) => {
      const payload = JSON.parse(event.data) as ClickEvent;
      setEvents((current) => [payload, ...current].slice(0, 10));
    });

    eventSource.addEventListener("error", () => {
      setStatus("error");
    });

    return () => {
      eventSource.close();
    };
  }, [token, workspaceId]);

  const liveCount = useMemo(() => events.length, [events]);

  return {
    status,
    events,
    liveCount,
  };
}
