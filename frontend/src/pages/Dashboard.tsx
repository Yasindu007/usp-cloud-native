import { useEffect } from "react";
import { Activity, Globe2, MonitorSmartphone, MousePointerClick } from "lucide-react";
import {
  Bar,
  BarChart,
  CartesianGrid,
  Legend,
  Line,
  LineChart,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import SectionHeading from "@/components/SectionHeading";
import MetricCard from "@/components/MetricCard";
import Card from "@/components/ui/Card";
import Select from "@/components/ui/Select";
import Skeleton from "@/components/ui/Skeleton";
import EmptyState from "@/components/ui/EmptyState";
import ErrorState from "@/components/ui/ErrorState";
import { useBreakdown, useSummaryWindows, useTimeSeries } from "@/hooks/useAnalytics";
import { useSessionStore } from "@/hooks/useSession";
import { useUrlList } from "@/hooks/useUrls";
import { formatDateTime, formatNumber, formatRelativeDays } from "@/utils/format";

const chartPalette = ["#f79209", "#0f766e", "#1d4ed8", "#ef4444", "#7c3aed"];

export default function Dashboard() {
  const selectedUrlId = useSessionStore((state) => state.selectedUrlId);
  const setSelectedUrlId = useSessionStore((state) => state.setSelectedUrlId);
  const urlList = useUrlList();

  useEffect(() => {
    if (!selectedUrlId && urlList.data?.data[0]?.id) {
      setSelectedUrlId(urlList.data.data[0].id);
    }
  }, [selectedUrlId, setSelectedUrlId, urlList.data]);

  const selectedUrl = urlList.data?.data.find((url) => url.id === selectedUrlId);
  const summaries = useSummaryWindows(selectedUrlId);
  const timeSeries = useTimeSeries(selectedUrlId);
  const deviceBreakdown = useBreakdown(selectedUrlId, "device_type");
  const countryBreakdown = useBreakdown(selectedUrlId, "country");

  const summaryLoading = summaries.some((query) => query.isLoading);
  const summaryError = summaries.find((query) => query.error)?.error?.message;

  return (
    <>
      <SectionHeading
        eyebrow="Analytics"
        title="Observe link traffic as an operator, not as a spectator"
        description="The dashboard centers on real backend telemetry: per-URL traffic windows, live counters, and country or device-level breakdowns for production decision-making."
      />

      <Card className="animate-rise">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
          <div>
            <h2 className="font-display text-2xl text-slate-950">Target URL</h2>
            <p className="mt-2 text-sm text-slate-600">
              The current backend exposes analytics per short URL, so operators select a URL as the dashboard target.
            </p>
          </div>
          <div className="w-full lg:max-w-md">
            <Select
              label="URL for analytics"
              value={selectedUrlId}
              onChange={(event) => setSelectedUrlId(event.target.value)}
            >
              <option value="">Select a URL</option>
              {urlList.data?.data.map((url) => (
                <option key={url.id} value={url.id}>
                  {(url.title || url.short_code).slice(0, 60)}
                </option>
              ))}
            </Select>
          </div>
        </div>
        {selectedUrl ? (
          <div className="mt-4 rounded-3xl bg-slate-950 px-5 py-4 text-slate-50">
            <p className="text-sm text-slate-300">Selected short URL</p>
            <p className="mt-2 break-all font-display text-2xl">{selectedUrl.short_url}</p>
            <p className="mt-2 text-sm text-slate-300">Created {formatDateTime(selectedUrl.created_at)}</p>
          </div>
        ) : null}
      </Card>

      {!selectedUrlId ? (
        <EmptyState
          title="Analytics need a target URL"
          description="Create a short URL first or pick one from the list above. The backend analytics endpoints are URL-scoped."
        />
      ) : (
        <>
          {summaryError ? <ErrorState message={summaryError} /> : null}

          <div className="grid gap-6 md:grid-cols-2 xl:grid-cols-4">
            {summaryLoading
              ? Array.from({ length: 4 }).map((_, index) => <Skeleton key={index} className="h-36" />)
              : [
                  {
                    label: "Total Clicks",
                    value: summaries[0].data?.total_clicks ?? 0,
                    icon: MousePointerClick,
                    tone: "bg-brand-100 text-brand-700",
                  },
                  {
                    label: "Last 24 Hours",
                    value: summaries[0].data?.total_clicks ?? 0,
                    icon: Activity,
                    tone: "bg-emerald-100 text-emerald-700",
                  },
                  {
                    label: "Last 7 Days",
                    value: summaries[1].data?.total_clicks ?? 0,
                    icon: MonitorSmartphone,
                    tone: "bg-cyan-100 text-cyan-700",
                  },
                  {
                    label: "Last 30 Days",
                    value: summaries[2].data?.total_clicks ?? 0,
                    icon: Globe2,
                    tone: "bg-violet-100 text-violet-700",
                  },
                ].map((metric) => <MetricCard key={metric.label} {...metric} />)}
          </div>

          <div className="grid gap-6 xl:grid-cols-[minmax(0,1.2fr)_minmax(320px,0.8fr)]">
            <Card className="animate-rise">
              <div className="flex items-center justify-between gap-3">
                <div>
                  <h3 className="font-display text-2xl text-slate-950">Traffic curve</h3>
                  <p className="mt-2 text-sm text-slate-600">
                    Daily click velocity for the last 30 days with unique IP overlay.
                  </p>
                </div>
                <div className="rounded-2xl bg-slate-100 px-3 py-2 text-sm text-slate-600">
                  {timeSeries.data ? `${formatDateTime(timeSeries.data.window_start)} onward` : "Awaiting data"}
                </div>
              </div>
              <div className="mt-6 h-[340px]">
                {timeSeries.isLoading ? (
                  <Skeleton className="h-full" />
                ) : timeSeries.error ? (
                  <ErrorState message={timeSeries.error.message} />
                ) : (
                  <ResponsiveContainer width="100%" height="100%">
                    <LineChart data={timeSeries.data?.points ?? []}>
                      <CartesianGrid strokeDasharray="3 3" stroke="#e2e8f0" />
                      <XAxis dataKey="bucket_start" tickFormatter={(value) => new Date(value).toLocaleDateString("en-US")} />
                      <YAxis />
                      <Tooltip
                        formatter={(value: number) => formatNumber(value)}
                        labelFormatter={(value) => formatDateTime(value)}
                      />
                      <Legend />
                      <Line type="monotone" dataKey="clicks" stroke={chartPalette[0]} strokeWidth={3} dot={false} />
                      <Line type="monotone" dataKey="unique_ips" stroke={chartPalette[1]} strokeWidth={2} dot={false} />
                    </LineChart>
                  </ResponsiveContainer>
                )}
              </div>
            </Card>

            <Card className="animate-rise">
              <h3 className="font-display text-2xl text-slate-950">Window snapshots</h3>
              <div className="mt-6 space-y-4">
                {summaries.map((query, index) => {
                  const label = ["24h", "7d", "30d"][index] as "24h" | "7d" | "30d";
                  return (
                    <div key={label} className="rounded-3xl border border-slate-200 bg-slate-50/70 p-4">
                      <div className="flex items-center justify-between gap-4">
                        <div>
                          <p className="font-semibold text-slate-900">{formatRelativeDays(label)}</p>
                          <p className="mt-1 text-sm text-slate-500">
                            {query.data ? `${formatNumber(query.data.unique_ips)} unique IPs` : "Loading"}
                          </p>
                        </div>
                        <p className="font-display text-2xl text-slate-950">
                          {formatNumber(query.data?.total_clicks ?? 0)}
                        </p>
                      </div>
                    </div>
                  );
                })}
              </div>
            </Card>
          </div>

          <div className="grid gap-6 xl:grid-cols-2">
            <Card className="animate-rise">
              <h3 className="font-display text-2xl text-slate-950">Device breakdown</h3>
              <p className="mt-2 text-sm text-slate-600">Device family split for the last 30 days.</p>
              <div className="mt-6 h-[320px]">
                {deviceBreakdown.isLoading ? (
                  <Skeleton className="h-full" />
                ) : deviceBreakdown.error ? (
                  <ErrorState message={deviceBreakdown.error.message} />
                ) : (
                  <ResponsiveContainer width="100%" height="100%">
                    <PieChart>
                      <Pie
                        data={deviceBreakdown.data?.counts ?? []}
                        dataKey="clicks"
                        nameKey="value"
                        innerRadius={70}
                        outerRadius={110}
                        paddingAngle={3}
                      />
                      <Tooltip formatter={(value: number) => formatNumber(value)} />
                      <Legend />
                    </PieChart>
                  </ResponsiveContainer>
                )}
              </div>
            </Card>

            <Card className="animate-rise">
              <h3 className="font-display text-2xl text-slate-950">Country breakdown</h3>
              <p className="mt-2 text-sm text-slate-600">Top geographic traffic sources across the same 30-day window.</p>
              <div className="mt-6 h-[320px]">
                {countryBreakdown.isLoading ? (
                  <Skeleton className="h-full" />
                ) : countryBreakdown.error ? (
                  <ErrorState message={countryBreakdown.error.message} />
                ) : (
                  <ResponsiveContainer width="100%" height="100%">
                    <BarChart data={countryBreakdown.data?.counts.slice(0, 8) ?? []} layout="vertical">
                      <CartesianGrid strokeDasharray="3 3" stroke="#e2e8f0" />
                      <XAxis type="number" />
                      <YAxis type="category" dataKey="value" width={80} />
                      <Tooltip formatter={(value: number) => formatNumber(value)} />
                      <Bar dataKey="clicks" fill={chartPalette[1]} radius={[0, 10, 10, 0]} />
                    </BarChart>
                  </ResponsiveContainer>
                )}
              </div>
            </Card>
          </div>
        </>
      )}
    </>
  );
}
