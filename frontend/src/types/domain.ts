export interface UrlRecord {
  id: string;
  short_url: string;
  short_code: string;
  original_url: string;
  title?: string;
  status: string;
  workspace_id: string;
  created_by: string;
  created_at: string;
  updated_at: string;
  expires_at?: string | null;
  click_count: number;
}

export interface ListUrlsResponse {
  data: UrlRecord[];
  meta: {
    cursor: string;
    has_more: boolean;
  };
}

export interface ShortenRequest {
  original_url: string;
  custom_code?: string;
  title?: string;
  expires_at?: string;
}

export interface ShortenResponse {
  id: string;
  short_url: string;
  short_code: string;
  original_url: string;
  workspace_id: string;
  created_at: string;
}

export interface Workspace {
  id: string;
  name: string;
  slug: string;
  plan_tier: string;
  owner_id: string;
  created_at: string;
  user_role: string;
}

export interface WorkspaceMember {
  user_id: string;
  role: string;
  joined_at: string;
  invited_by: string;
}

export interface ApiKeySummary {
  id: string;
  name: string;
  key_prefix: string;
  scopes: string[];
  created_at: string;
  expires_at?: string | null;
  last_used_at?: string | null;
}

export interface CreateApiKeyPayload {
  name: string;
  scopes: string[];
  expires_at?: string;
}

export interface CreateApiKeyResponse {
  id: string;
  name: string;
  key_prefix: string;
  raw_key: string;
  store_now: string;
  scopes: string[];
  workspace_id: string;
  created_at: string;
  expires_at?: string | null;
}

export interface AnalyticsSummary {
  short_code: string;
  short_url: string;
  total_clicks: number;
  unique_ips: number;
  bot_clicks: number;
  window: string;
  window_start: string;
  window_end: string;
}

export interface AnalyticsPoint {
  bucket_start: string;
  clicks: number;
  unique_ips: number;
}

export interface AnalyticsTimeSeries {
  short_code: string;
  granularity: string;
  window_start: string;
  window_end: string;
  points: AnalyticsPoint[];
}

export interface BreakdownCount {
  value: string;
  clicks: number;
  percentage: number;
}

export interface AnalyticsBreakdown {
  short_code: string;
  dimension: string;
  total_clicks: number;
  window_start: string;
  window_end: string;
  counts: BreakdownCount[];
}

export interface ClickEvent {
  short_code?: string;
  url_id?: string;
  workspace_id?: string;
  country?: string;
  device_type?: string;
  browser_family?: string;
  os_family?: string;
  referrer_domain?: string;
  occurred_at?: string;
  timestamp?: string;
  ip_hash?: string;
  user_agent?: string;
  is_bot?: boolean;
}
