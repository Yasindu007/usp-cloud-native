import { NavLink } from "react-router-dom";
import { Activity, ArrowUpRight, CloudCog } from "lucide-react";
import type { NavItem } from "@/layout/AppLayout";
import { useSessionStore } from "@/hooks/useSession";
import { cn } from "@/utils/cn";
import Badge from "@/components/ui/Badge";

export default function Sidebar({ navItems }: { navItems: NavItem[] }) {
  const workspaceId = useSessionStore((state) => state.workspaceId);

  return (
    <aside className="panel-muted sticky top-4 hidden h-[calc(100vh-2rem)] flex-col overflow-hidden p-6 lg:flex">
      <div className="rounded-3xl bg-white/5 p-5">
        <div className="flex items-center gap-3">
          <div className="rounded-2xl bg-brand-400/20 p-3 text-brand-300">
            <CloudCog className="h-6 w-6" />
          </div>
          <div>
            <p className="text-xs font-semibold uppercase tracking-[0.22em] text-brand-300">Cloud Native</p>
            <h1 className="font-display text-xl text-white">USP Control Plane</h1>
          </div>
        </div>
        <p className="mt-4 text-sm text-slate-300">
          SaaS control surface for routing, analytics, and workspace operations.
        </p>
      </div>

      <nav className="mt-8 flex flex-col gap-2">
        {navItems.map((item) => (
          <NavLink
            key={item.to}
            to={item.to}
            className={({ isActive }) =>
              cn(
                "flex items-center gap-3 rounded-2xl px-4 py-3 text-sm font-medium transition",
                isActive ? "bg-white text-slate-950" : "text-slate-300 hover:bg-white/5 hover:text-white",
              )
            }
          >
            <item.icon className="h-4 w-4" />
            {item.label}
          </NavLink>
        ))}
      </nav>

      <div className="mt-auto space-y-4">
        <div className="rounded-3xl border border-white/10 bg-white/5 p-4">
          <div className="flex items-center justify-between gap-3">
            <div>
              <p className="text-xs uppercase tracking-[0.24em] text-slate-400">Active Workspace</p>
              <p className="mt-2 text-sm font-semibold text-white">{workspaceId || "Not configured"}</p>
            </div>
            <Badge tone={workspaceId ? "success" : "warning"}>
              {workspaceId ? "Ready" : "Pending"}
            </Badge>
          </div>
        </div>

        <div className="rounded-3xl bg-brand-400/15 p-4">
          <div className="flex items-start gap-3">
            <Activity className="mt-0.5 h-5 w-5 text-brand-300" />
            <div>
              <p className="font-semibold text-white">Ops posture</p>
              <p className="mt-2 text-sm text-slate-300">
                Live telemetry, per-URL analytics, and workspace auth are wired into one operator surface.
              </p>
            </div>
          </div>
          <div className="mt-4 inline-flex items-center gap-2 text-sm font-medium text-brand-300">
            Production-minded UI
            <ArrowUpRight className="h-4 w-4" />
          </div>
        </div>
      </div>
    </aside>
  );
}
