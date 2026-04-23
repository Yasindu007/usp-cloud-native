import { Activity, RadioTower, Waves } from "lucide-react";
import SectionHeading from "@/components/SectionHeading";
import Card from "@/components/ui/Card";
import EmptyState from "@/components/ui/EmptyState";
import Badge from "@/components/ui/Badge";
import { useLiveClicks } from "@/hooks/useLiveClicks";
import { useSessionStore } from "@/hooks/useSession";
import { formatDateTime } from "@/utils/format";

export default function LiveStream() {
  const workspaceId = useSessionStore((state) => state.workspaceId);
  const token = useSessionStore((state) => state.token);
  const { status, events, liveCount } = useLiveClicks();

  return (
    <>
      <SectionHeading
        eyebrow="Real-Time"
        title="Watch click telemetry arrive in real time"
        description="This view subscribes to the workspace SSE stream and surfaces the most recent click events for operator awareness."
      />

      <div className="grid gap-6 xl:grid-cols-[340px_minmax(0,1fr)]">
        <Card className="panel-muted animate-rise">
          <div className="flex items-start justify-between gap-4">
            <div>
              <p className="text-xs font-semibold uppercase tracking-[0.24em] text-brand-300">Stream Status</p>
              <h2 className="mt-2 font-display text-3xl">{status}</h2>
            </div>
            <RadioTower className="h-8 w-8 text-brand-300" />
          </div>

          <div className="mt-8 rounded-3xl bg-white/5 p-5">
            <p className="text-sm text-slate-300">Last 10 click events</p>
            <p className="mt-2 font-display text-4xl">{liveCount}</p>
          </div>

          <div className="mt-5 flex flex-wrap gap-3">
            <Badge tone={status === "open" ? "success" : "warning"}>{status}</Badge>
            <Badge>{workspaceId || "workspace missing"}</Badge>
          </div>

          <p className="mt-5 text-sm text-slate-300">
            The stream uses an EventSource-compatible client so Bearer auth can be sent to the current backend.
          </p>
        </Card>

        <Card className="animate-rise">
          {!workspaceId || !token ? (
            <EmptyState
              title="Session details are required"
              description="Provide a workspace ID and JWT token in Settings before opening the live stream."
            />
          ) : !events.length ? (
            <EmptyState
              title="Connected, waiting for clicks"
              description="Trigger redirects against the workspace to see events stream into the dashboard."
            />
          ) : (
            <>
              <div className="flex items-center justify-between gap-3">
                <div>
                  <h3 className="font-display text-2xl text-slate-950">Recent click events</h3>
                  <p className="mt-2 text-sm text-slate-600">Latest non-bot events from the workspace SSE feed.</p>
                </div>
                <div className="rounded-2xl bg-brand-100 p-3 text-brand-700">
                  <Waves className="h-5 w-5" />
                </div>
              </div>

              <div className="mt-6 space-y-3">
                {events.map((event, index) => (
                  <div key={`${event.short_code ?? "evt"}-${index}`} className="rounded-3xl border border-slate-200 p-5">
                    <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
                      <div>
                        <div className="flex flex-wrap items-center gap-3">
                          <p className="font-display text-xl text-slate-950">{event.short_code || "unknown"}</p>
                          <Badge tone="success">{event.device_type || "device n/a"}</Badge>
                          <Badge>{event.country || "country n/a"}</Badge>
                        </div>
                        <p className="mt-2 text-sm text-slate-500">
                          Referrer: {event.referrer_domain || "direct"} • Browser: {event.browser_family || "unknown"}
                        </p>
                      </div>
                      <div className="flex items-center gap-2 text-sm text-slate-500">
                        <Activity className="h-4 w-4" />
                        {formatDateTime(event.occurred_at || event.timestamp)}
                      </div>
                    </div>
                  </div>
                ))}
              </div>
            </>
          )}
        </Card>
      </div>
    </>
  );
}
