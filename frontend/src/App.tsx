import { Suspense, lazy } from "react";
import { Navigate, Route, Routes } from "react-router-dom";
import { BarChart3, Link2, RadioTower, Settings2 } from "lucide-react";
import AppLayout from "@/layout/AppLayout";
import Skeleton from "@/components/ui/Skeleton";

const Dashboard = lazy(() => import("@/pages/Dashboard"));
const LiveStream = lazy(() => import("@/pages/LiveStream"));
const Settings = lazy(() => import("@/pages/Settings"));
const Shortener = lazy(() => import("@/pages/Shortener"));

const navItems = [
  { to: "/dashboard", label: "Analytics", icon: BarChart3 },
  { to: "/shortener", label: "Shortener", icon: Link2 },
  { to: "/stream", label: "Live Stream", icon: RadioTower },
  { to: "/settings", label: "Settings", icon: Settings2 },
];

export default function App() {
  return (
    <Suspense fallback={<div className="space-y-4"><Skeleton className="h-32" /><Skeleton className="h-96" /></div>}>
      <Routes>
        <Route element={<AppLayout navItems={navItems} />}>
          <Route path="/" element={<Navigate to="/dashboard" replace />} />
          <Route path="/dashboard" element={<Dashboard />} />
          <Route path="/shortener" element={<Shortener />} />
          <Route path="/stream" element={<LiveStream />} />
          <Route path="/settings" element={<Settings />} />
        </Route>
      </Routes>
    </Suspense>
  );
}
