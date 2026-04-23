import type { PropsWithChildren } from "react";
import { cn } from "@/utils/cn";

interface BadgeProps {
  tone?: "default" | "success" | "warning";
  className?: string;
}

const tones = {
  default: "bg-slate-100 text-slate-700",
  success: "bg-emerald-100 text-emerald-700",
  warning: "bg-amber-100 text-amber-800",
};

export default function Badge({ children, tone = "default", className }: PropsWithChildren<BadgeProps>) {
  return (
    <span className={cn("inline-flex rounded-full px-3 py-1 text-xs font-semibold", tones[tone], className)}>
      {children}
    </span>
  );
}
