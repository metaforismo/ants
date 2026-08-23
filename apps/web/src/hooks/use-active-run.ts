"use client";

import { useCallback, useSyncExternalStore } from "react";

/**
 * Anchors the most recently started or observed run of a thread, per
 * browser tab. `/v1` exposes runs by id only (no list-by-thread yet), so
 * this is how the workspace re-opens the live panel after an in-tab
 * reload. Reopening the thread elsewhere shows the truthful thread status
 * while the run completes without a live panel; closing that gap needs a
 * list-runs-by-thread API and is named in the tranche evidence.
 *
 * sessionStorage is an external system, so the hook subscribes to a tiny
 * store instead of mirroring it into component state on mount.
 */

type Listener = () => void;

const PREFIX = "ants_active_run:";
const listeners = new Map<string, Set<Listener>>();

function listenersFor(key: string): Set<Listener> {
  let set = listeners.get(key);
  if (!set) {
    set = new Set();
    listeners.set(key, set);
  }
  return set;
}

function subscribe(key: string, listener: Listener): () => void {
  const set = listenersFor(key);
  set.add(listener);
  return () => {
    set.delete(listener);
  };
}

function notify(key: string): void {
  for (const listener of listenersFor(key)) listener();
}

function readSnapshot(key: string): string | null {
  try {
    // The value is validated at write time; storage can only hand back
    // what this module put there.
    return window.sessionStorage.getItem(key);
  } catch {
    // Storage unavailable (e.g. hardened privacy mode): the panel stays
    // closed; nothing else about the workspace depends on it.
    return null;
  }
}

export function useActiveRun(threadId: string): {
  runId: string | undefined;
  setActiveRun: (id: string) => void;
} {
  const key = `${PREFIX}${threadId}`;

  const subscribeKeyed = useCallback(
    (listener: Listener) => subscribe(key, listener),
    [key],
  );
  const getSnapshot = useCallback(() => readSnapshot(key), [key]);
  const getServerSnapshot = useCallback((): null => null, []);

  const stored = useSyncExternalStore(subscribeKeyed, getSnapshot, getServerSnapshot);

  const setActiveRun = useCallback(
    (id: string) => {
      try {
        window.sessionStorage.setItem(key, id);
        notify(key);
      } catch {
        // Same deliberate degradation as the read path above.
      }
    },
    [key],
  );

  return { runId: stored ?? undefined, setActiveRun };
}
