import axios from "axios";
import { env } from "@/utils/env";
import { useSessionStore } from "@/hooks/useSession";
import type { Envelope, ProblemDetails } from "@/types/api";

export const apiClient = axios.create({
  baseURL: env.apiUrl,
  headers: {
    "Content-Type": "application/json",
  },
});

apiClient.interceptors.request.use((config) => {
  const token = useSessionStore.getState().token;

  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }

  return config;
});

apiClient.interceptors.response.use(
  (response) => response,
  (error) => {
    const detail =
      (error.response?.data as ProblemDetails | undefined)?.detail ??
      error.response?.data?.title ??
      error.message ??
      "Request failed";

    return Promise.reject(new Error(detail));
  },
);

export async function unwrapEnvelope<T>(promise: Promise<{ data: Envelope<T> }>) {
  const response = await promise;
  return response.data.data;
}
