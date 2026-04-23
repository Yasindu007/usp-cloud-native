import { Link } from "react-router-dom";
import { Bell, Search, Settings } from "lucide-react";
import Input from "@/components/ui/Input";
import { env } from "@/utils/env";
import { useSessionStore } from "@/hooks/useSession";

export default function TopBar() {
  const workspaceId = useSessionStore((state) => state.workspaceId);
  const userId = useSessionStore((state) => state.userId);

  return (
    <header className="panel animate-rise p-4 sm:p-5">
      <div className="flex flex-col gap-4 xl:flex-row xl:items-center xl:justify-between">
        <div>
          <p className="text-xs font-semibold uppercase tracking-[0.24em] text-brand-700">{env.appTitle}</p>
          <div className="mt-2 flex flex-wrap items-center gap-3 text-sm text-slate-600">
            <span>Workspace: {workspaceId || "unset"}</span>
            <span className="hidden text-slate-300 sm:inline">•</span>
            <span>User: {userId || "unset"}</span>
          </div>
        </div>

        <div className="flex flex-col gap-3 sm:flex-row sm:items-center">
          <div className="relative min-w-[240px]">
            <Search className="pointer-events-none absolute left-4 top-1/2 h-4 w-4 -translate-y-1/2 text-slate-400" />
            <Input className="pl-10" placeholder="Observability, keys, URLs" aria-label="Search" />
          </div>
          <div className="flex items-center gap-3">
            <button className="rounded-2xl bg-slate-100 p-3 text-slate-600 transition hover:bg-slate-200">
              <Bell className="h-4 w-4" />
            </button>
            <Link
              to="/settings"
              className="inline-flex items-center gap-2 rounded-2xl bg-slate-900 px-4 py-3 text-sm font-semibold text-white"
            >
              <Settings className="h-4 w-4" />
              Session
            </Link>
          </div>
        </div>
      </div>
    </header>
  );
}
