const defaultApiUrl = "http://localhost:8080";

export const env = {
  apiUrl: (import.meta.env.VITE_API_URL as string | undefined) ?? defaultApiUrl,
  appTitle: (import.meta.env.VITE_APP_TITLE as string | undefined) ?? "USP Control Plane",
};
