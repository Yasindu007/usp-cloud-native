import { useState } from "react";
import { Link2, Rocket, Shield } from "lucide-react";
import SectionHeading from "@/components/SectionHeading";
import Card from "@/components/ui/Card";
import Input from "@/components/ui/Input";
import Button from "@/components/ui/Button";
import CopyButton from "@/components/ui/CopyButton";
import EmptyState from "@/components/ui/EmptyState";
import { useCreateUrl, useUrlList } from "@/hooks/useUrls";
import { useSessionStore } from "@/hooks/useSession";
import { formatDateTime } from "@/utils/format";
import Badge from "@/components/ui/Badge";

export default function Shortener() {
  const workspaceId = useSessionStore((state) => state.workspaceId);
  const createUrl = useCreateUrl();
  const urlList = useUrlList();
  const [form, setForm] = useState({
    original_url: "",
    custom_code: "",
    title: "",
  });

  return (
    <>
      <SectionHeading
        eyebrow="Create URLs"
        title="Release links without losing operational control"
        description="Provision short URLs from the same workspace that carries analytics, audit context, and live click telemetry."
      />

      <div className="grid gap-6 xl:grid-cols-[minmax(0,1.15fr)_400px]">
        <Card className="animate-rise">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h2 className="font-display text-2xl text-slate-950">URL Shortener</h2>
              <p className="mt-2 text-sm text-slate-600">
                Requests are sent to the workspace-scoped shorten endpoint with JWT auth attached automatically.
              </p>
            </div>
            <div className="rounded-2xl bg-brand-100 p-3 text-brand-700">
              <Link2 className="h-6 w-6" />
            </div>
          </div>

          <form
            className="mt-8 grid gap-4"
            onSubmit={(event) => {
              event.preventDefault();
              createUrl.mutate({
                original_url: form.original_url,
                custom_code: form.custom_code || undefined,
                title: form.title || undefined,
              });
            }}
          >
            <Input
              label="Original URL"
              type="url"
              required
              value={form.original_url}
              onChange={(event) => setForm((current) => ({ ...current, original_url: event.target.value }))}
              placeholder="https://platform.example.com/docs/deployment-runbook"
            />
            <div className="grid gap-4 md:grid-cols-2">
              <Input
                label="Custom shortcode"
                value={form.custom_code}
                onChange={(event) => setForm((current) => ({ ...current, custom_code: event.target.value }))}
                placeholder="launch-q2"
                hint="Optional. Letters, numbers, hyphen, underscore."
              />
              <Input
                label="Title"
                value={form.title}
                onChange={(event) => setForm((current) => ({ ...current, title: event.target.value }))}
                placeholder="Deployment Runbook"
              />
            </div>
            <div className="flex flex-wrap items-center gap-3">
              <Button type="submit" disabled={!workspaceId || createUrl.isPending} className="gap-2">
                <Rocket className="h-4 w-4" />
                {createUrl.isPending ? "Creating..." : "Create short URL"}
              </Button>
              {!workspaceId ? <span className="text-sm text-amber-700">Configure your workspace first.</span> : null}
            </div>
          </form>

          {createUrl.data ? (
            <div className="mt-8 rounded-3xl border border-emerald-200 bg-emerald-50 p-5">
              <div className="flex flex-col gap-4 md:flex-row md:items-center md:justify-between">
                <div>
                  <p className="text-sm font-medium text-emerald-700">Generated short URL</p>
                  <a
                    href={createUrl.data.short_url}
                    target="_blank"
                    rel="noreferrer"
                    className="mt-2 block break-all font-display text-2xl text-slate-950"
                  >
                    {createUrl.data.short_url}
                  </a>
                  <p className="mt-2 text-sm text-slate-600">Created at {formatDateTime(createUrl.data.created_at)}</p>
                </div>
                <CopyButton value={createUrl.data.short_url} label="Copy URL" />
              </div>
            </div>
          ) : null}
        </Card>

        <div className="space-y-6">
          <Card className="animate-rise">
            <div className="flex items-start gap-3">
              <div className="rounded-2xl bg-ocean-500/10 p-3 text-ocean-600">
                <Shield className="h-5 w-5" />
              </div>
              <div>
                <h3 className="font-display text-xl text-slate-950">Operational notes</h3>
                <p className="mt-2 text-sm text-slate-600">
                  Custom codes are useful for campaign launches, incident runbooks, and human-readable support flows.
                </p>
              </div>
            </div>
            <ul className="mt-5 space-y-3 text-sm text-slate-600">
              <li>JWT is sent as `Authorization: Bearer ...` through a shared Axios interceptor.</li>
              <li>Workspace scoping keeps URL creation isolated per tenant.</li>
              <li>Successful creates invalidate the URL list so analytics selectors stay current.</li>
            </ul>
          </Card>

          <Card className="animate-rise">
            <div className="flex items-center justify-between gap-4">
              <div>
                <h3 className="font-display text-xl text-slate-950">Recent URLs</h3>
                <p className="mt-2 text-sm text-slate-600">Freshly created links available for analytics inspection.</p>
              </div>
              <Badge>{urlList.data?.data.length ?? 0} URLs</Badge>
            </div>
            <div className="mt-5 space-y-3">
              {urlList.data?.data.slice(0, 4).map((url) => (
                <div key={url.id} className="rounded-2xl border border-slate-200 p-4">
                  <div className="flex items-center justify-between gap-4">
                    <div className="min-w-0">
                      <p className="truncate font-semibold text-slate-900">{url.title || url.short_code}</p>
                      <p className="truncate text-sm text-slate-500">{url.original_url}</p>
                    </div>
                    <Badge tone={url.status === "active" ? "success" : "warning"}>{url.status}</Badge>
                  </div>
                </div>
              ))}
              {!urlList.data?.data.length ? (
                <EmptyState
                  title="No URLs yet"
                  description="Create your first link to unlock the analytics and live stream pages."
                />
              ) : null}
            </div>
          </Card>
        </div>
      </div>
    </>
  );
}
