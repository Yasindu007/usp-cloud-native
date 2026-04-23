import type { SelectHTMLAttributes } from "react";
import { cn } from "@/utils/cn";

interface SelectProps extends SelectHTMLAttributes<HTMLSelectElement> {
  label?: string;
}

export default function Select({ label, className, children, ...props }: SelectProps) {
  return (
    <label className="flex flex-col gap-2 text-sm text-slate-600">
      {label ? <span className="font-medium text-slate-700">{label}</span> : null}
      <select
        className={cn(
          "w-full rounded-2xl border border-slate-200 bg-white px-4 py-3 text-sm text-slate-900 shadow-sm outline-none transition focus:border-brand-400 focus:ring-4 focus:ring-brand-100",
          className,
        )}
        {...props}
      >
        {children}
      </select>
    </label>
  );
}
