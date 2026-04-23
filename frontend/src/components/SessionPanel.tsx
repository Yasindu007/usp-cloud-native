import { useState } from "react";
import { ShieldCheck, Trash2 } from "lucide-react";
import Button from "@/components/ui/Button";
import Card from "@/components/ui/Card";
import Input from "@/components/ui/Input";
import { useSessionStore } from "@/hooks/useSession";

export default function SessionPanel() {
  const { token, workspaceId, userId, setSession, clearSession } = useSessionStore();
  const [draft, setDraft] = useState({
    token,
    workspaceId,
    userId,
  });

  return (
    <Card className="panel-muted">
      <div className="flex flex-col gap-6">
        <div className="flex items-start justify-between gap-4">
          <div>
            <p className="text-xs font-semibold uppercase tracking-[0.24em] text-brand-300">Session</p>
            <h2 className="mt-2 font-display text-2xl">Access profile</h2>
            <p className="mt-2 max-w-2xl text-sm text-slate-300">
              The dashboard stores the JWT in local storage and injects it into every API request.
            </p>
          </div>
          <ShieldCheck className="h-8 w-8 text-brand-300" />
        </div>

        <div className="grid gap-4 lg:grid-cols-3">
          <Input
            label="Workspace ID"
            value={draft.workspaceId}
            onChange={(event) => setDraft((current) => ({ ...current, workspaceId: event.target.value }))}
            placeholder="ws_..."
          />
          <Input
            label="User ID"
            value={draft.userId}
            onChange={(event) => setDraft((current) => ({ ...current, userId: event.target.value }))}
            placeholder="usr_..."
          />
          <Input
            label="Bearer Token"
            value={draft.token}
            onChange={(event) => setDraft((current) => ({ ...current, token: event.target.value }))}
            placeholder="eyJhbGciOiJSUzI1NiIs..."
          />
        </div>

        <div className="flex flex-wrap gap-3">
          <Button onClick={() => setSession(draft)}>Save Session</Button>
          <Button variant="ghost" onClick={() => clearSession()} className="gap-2">
            <Trash2 className="h-4 w-4" />
            Clear
          </Button>
        </div>
      </div>
    </Card>
  );
}
