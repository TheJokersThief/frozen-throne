import { useSyncExternalStore } from "react";

/** Session/permission events raised centrally by the QueryClient (see queryClient.ts). */
let expired = false;
const subs = new Set<() => void>();
const emit = () => subs.forEach((f) => f());
const subscribe = (f: () => void) => {
  subs.add(f);
  return () => void subs.delete(f);
};

export const session = {
  isExpired: () => expired,
  setExpired(v: boolean) {
    if (expired === v) return;
    expired = v;
    emit();
  },
};

export function useSessionExpired(): boolean {
  return useSyncExternalStore(subscribe, session.isExpired);
}

export type ApiEvent =
  | { type: "forbidden"; action: string; message: string }
  | { type: "rejected"; message: string };

const listeners = new Set<(e: ApiEvent) => void>();
export const apiEvents = {
  emit: (e: ApiEvent) => listeners.forEach((f) => f(e)),
  on(f: (e: ApiEvent) => void) {
    listeners.add(f);
    return () => void listeners.delete(f);
  },
};
