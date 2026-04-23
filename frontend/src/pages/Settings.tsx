import { useState } from "react";
import { KeyRound, Users2 } from "lucide-react";
import SectionHeading from "@/components/SectionHeading";
import SessionPanel from "@/components/SessionPanel";
import Card from "@/components/ui/Card";
import Button from "@/components/ui/Button";
import Input from "@/components/ui/Input";
import Badge from "@/components/ui/Badge";
import CopyButton from "@/components/ui/CopyButton";
import EmptyState from "@/components/ui/EmptyState";
import ErrorState from "@/components/ui/ErrorState";
import { useApiKeys, useCreateApiKey, useWorkspaceDetails, useWorkspaceMembers } from "@/hooks/useWorkspace";
import { formatDateTime, truncateMiddle } from "@/utils/format";

export default function Settings() {
  const workspace = useWorkspaceDetails();
  const members = useWorkspaceMembers();
  const apiKeys = useApiKeys();
  const createApiKey = useCreateApiKey();
  const [form, setForm] = useState({
    name: "",
    scopes: "urls:read,urls:write,analytics:read",
    expires_at: "",
  });

  return (
    <>
      <SectionHeading
        eyebrow="Settings"
        title="Workspace administration and access surface"
        description="Review workspace identity, membership, and API key inventory from the same operator console."
      />

      <SessionPanel />

      <div className="grid gap-6 xl:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)]">
        <Card className="animate-rise">
          <div className="flex items-center gap-3">
            <div className="rounded-2xl bg-slate-100 p-3 text-slate-700">
              <Users2 className="h-5 w-5" />
            </div>
            <div>
              <h2 className="font-display text-2xl text-slate-950">Workspace</h2>
              <p className="mt-1 text-sm text-slate-600">Identity and membership context from the backend.</p>
            </div>
          </div>

          <div className="mt-6 space-y-4">
            {workspace.isLoading ? (
              <div className="space-y-3">
                <div className="h-20 animate-pulse rounded-2xl bg-slate-200" />
                <div className="h-20 animate-pulse rounded-2xl bg-slate-200" />
              </div>
            ) : workspace.error ? (
              <ErrorState message={workspace.error.message} />
            ) : workspace.data ? (
              <>
                <div className="rounded-3xl border border-slate-200 p-5">
                  <p className="text-xs uppercase tracking-[0.24em] text-slate-400">Workspace ID</p>
                  <p className="mt-2 break-all font-display text-2xl text-slate-950">{workspace.data.id}</p>
                  <div className="mt-4 flex flex-wrap gap-3">
                    <Badge>{workspace.data.plan_tier}</Badge>
                    <Badge tone="success">{workspace.data.user_role}</Badge>
                  </div>
                </div>
                <div className="grid gap-4 md:grid-cols-2">
                  <div className="rounded-3xl bg-slate-50 p-5">
                    <p className="text-sm text-slate-500">Name</p>
                    <p className="mt-2 font-semibold text-slate-900">{workspace.data.name}</p>
                  </div>
                  <div className="rounded-3xl bg-slate-50 p-5">
                    <p className="text-sm text-slate-500">Owner</p>
                    <p className="mt-2 font-semibold text-slate-900">{workspace.data.owner_id}</p>
                  </div>
                </div>
              </>
            ) : (
              <EmptyState title="Workspace missing" description="Add session values to load workspace settings." />
            )}
          </div>

          <div className="mt-8">
            <h3 className="font-display text-xl text-slate-950">Members</h3>
            <div className="mt-4 space-y-3">
              {members.data?.map((member) => (
                <div key={member.user_id} className="rounded-2xl border border-slate-200 p-4">
                  <div className="flex items-center justify-between gap-3">
                    <div>
                      <p className="font-semibold text-slate-900">{member.user_id}</p>
                      <p className="mt-1 text-sm text-slate-500">
                        Joined {formatDateTime(member.joined_at)} • Invited by {member.invited_by}
                      </p>
                    </div>
                    <Badge>{member.role}</Badge>
                  </div>
                </div>
              ))}
              {!members.data?.length && !members.isLoading ? (
                <EmptyState title="No members returned" description="This workspace may be inaccessible or empty." />
              ) : null}
            </div>
          </div>
        </Card>

        <Card className="animate-rise">
          <div className="flex items-center gap-3">
            <div className="rounded-2xl bg-brand-100 p-3 text-brand-700">
              <KeyRound className="h-5 w-5" />
            </div>
            <div>
              <h2 className="font-display text-2xl text-slate-950">API keys</h2>
              <p className="mt-1 text-sm text-slate-600">List current keys and generate a new one for automation.</p>
            </div>
          </div>

          <form
            className="mt-6 grid gap-4 rounded-3xl border border-slate-200 bg-slate-50/70 p-5"
            onSubmit={(event) => {
              event.preventDefault();
              createApiKey.mutate({
                name: form.name,
                scopes: form.scopes
                  .split(",")
                  .map((scope) => scope.trim())
                  .filter(Boolean),
                expires_at: form.expires_at ? new Date(form.expires_at).toISOString() : undefined,
              });
            }}
          >
            <Input
              label="Key name"
              value={form.name}
              onChange={(event) => setForm((current) => ({ ...current, name: event.target.value }))}
              placeholder="grafana-exporter"
              required
            />
            <Input
              label="Scopes"
              value={form.scopes}
              onChange={(event) => setForm((current) => ({ ...current, scopes: event.target.value }))}
              placeholder="urls:read,analytics:read"
            />
            <Input
              label="Expires at"
              type="datetime-local"
              value={form.expires_at}
              onChange={(event) => setForm((current) => ({ ...current, expires_at: event.target.value }))}
            />
            <div>
              <Button type="submit" disabled={createApiKey.isPending}>
                {createApiKey.isPending ? "Creating..." : "Create API key"}
              </Button>
            </div>
          </form>

          {createApiKey.data ? (
            <div className="mt-5 rounded-3xl border border-emerald-200 bg-emerald-50 p-5">
              <p className="text-sm font-medium text-emerald-700">Raw key</p>
              <p className="mt-2 break-all font-mono text-sm text-slate-900">{createApiKey.data.raw_key}</p>
              <div className="mt-4">
                <CopyButton value={createApiKey.data.raw_key} label="Copy raw key" />
              </div>
            </div>
          ) : null}

          <div className="mt-8 space-y-3">
            {apiKeys.error ? <ErrorState message={apiKeys.error.message} /> : null}
            {apiKeys.data?.map((key) => (
              <div key={key.id} className="rounded-3xl border border-slate-200 p-5">
                <div className="flex flex-col gap-4 lg:flex-row lg:items-center lg:justify-between">
                  <div>
                    <p className="font-semibold text-slate-900">{key.name}</p>
                    <p className="mt-1 text-sm text-slate-500">{truncateMiddle(key.key_prefix, 6)}</p>
                  </div>
                  <div className="flex flex-wrap gap-2">
                    {key.scopes.map((scope) => (
                      <Badge key={scope}>{scope}</Badge>
                    ))}
                  </div>
                </div>
                <div className="mt-4 flex flex-wrap gap-5 text-sm text-slate-500">
                  <span>Created {formatDateTime(key.created_at)}</span>
                  <span>Expires {key.expires_at ? formatDateTime(key.expires_at) : "never"}</span>
                  <span>Last used {key.last_used_at ? formatDateTime(key.last_used_at) : "not yet"}</span>
                </div>
              </div>
            ))}
            {!apiKeys.data?.length && !apiKeys.isLoading ? (
              <EmptyState
                title="No API keys yet"
                description="Create one above for Grafana exports, CI/CD hooks, or service-to-service access."
              />
            ) : null}
          </div>
        </Card>
      </div>
    </>
  );
}
