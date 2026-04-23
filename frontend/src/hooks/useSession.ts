import { create } from "zustand";
import { persist } from "zustand/middleware";

interface SessionStore {
  token: string;
  workspaceId: string;
  userId: string;
  selectedUrlId: string;
  setSession: (session: { token: string; workspaceId: string; userId: string }) => void;
  clearSession: () => void;
  setSelectedUrlId: (urlId: string) => void;
}

export const useSessionStore = create<SessionStore>()(
  persist(
    (set) => ({
      token: "",
      workspaceId: "",
      userId: "",
      selectedUrlId: "",
      setSession: ({ token, workspaceId, userId }) => set({ token, workspaceId, userId }),
      clearSession: () => set({ token: "", workspaceId: "", userId: "", selectedUrlId: "" }),
      setSelectedUrlId: (selectedUrlId) => set({ selectedUrlId }),
    }),
    {
      name: "usp-control-plane-session",
      partialize: (state) => ({
        token: state.token,
        workspaceId: state.workspaceId,
        userId: state.userId,
        selectedUrlId: state.selectedUrlId,
      }),
    },
  ),
);
