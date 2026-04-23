import { Outlet } from "react-router-dom";
import type { LucideIcon } from "lucide-react";
import Sidebar from "@/layout/Sidebar";
import TopBar from "@/layout/TopBar";

export interface NavItem {
  to: string;
  label: string;
  icon: LucideIcon;
}

export default function AppLayout({ navItems }: { navItems: NavItem[] }) {
  return (
    <div className="min-h-screen">
      <div className="mx-auto grid min-h-screen max-w-[1600px] grid-cols-1 gap-6 px-4 py-4 lg:grid-cols-[280px_minmax(0,1fr)] lg:px-6">
        <Sidebar navItems={navItems} />
        <main className="min-w-0">
          <TopBar />
          <div className="mt-6 flex flex-col gap-6 pb-8">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}
