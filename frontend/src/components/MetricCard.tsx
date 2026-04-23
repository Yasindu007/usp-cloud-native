import type { LucideIcon } from "lucide-react";
import Card from "@/components/ui/Card";
import { formatNumber } from "@/utils/format";

interface MetricCardProps {
  label: string;
  value: number;
  icon: LucideIcon;
  tone: string;
}

export default function MetricCard({ label, value, icon: Icon, tone }: MetricCardProps) {
  return (
    <Card className="overflow-hidden">
      <div className="flex items-start justify-between gap-4">
        <div>
          <p className="text-sm font-medium text-slate-500">{label}</p>
          <p className="mt-3 font-display text-3xl text-slate-950">{formatNumber(value)}</p>
        </div>
        <div className={`rounded-2xl p-3 ${tone}`}>
          <Icon className="h-5 w-5" />
        </div>
      </div>
    </Card>
  );
}
