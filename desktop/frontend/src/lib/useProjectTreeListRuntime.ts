import { useCallback, useRef, useState } from "react";
import {
  createProjectTreeRequestLimiter,
  projectTreeRuntimeWindowLimits,
  resetProjectTreeRuntimeWindowLimits,
  type ProjectTreeListPageState,
} from "./projectTreeWindow";

export function useProjectTreeListRuntime() {
  const topicRevisionRef = useRef<Record<string, number>>({});
  const topicCompletePageRef = useRef<Record<string, { signature: string; revision: number }>>({});
  const [topicPageState, setTopicPageState] = useState<Record<string, ProjectTreeListPageState>>({});
  const topicPageStateRef = useRef(topicPageState);
  const updateTopicPageState = useCallback((key: string, next: ProjectTreeListPageState) => {
    // Publish synchronously so sibling effects see the same request/cache state
    // before React commits the corresponding render.
    const updated = { ...topicPageStateRef.current, [key]: next };
    topicPageStateRef.current = updated;
    setTopicPageState(updated);
  }, []);

  const [topicWindowLimits, setTopicWindowLimits] = useState<Record<string, number>>(projectTreeRuntimeWindowLimits);
  const topicWindowLimitsRef = useRef(topicWindowLimits);
  topicWindowLimitsRef.current = topicWindowLimits;
  const resetTopicWindowLimits = useCallback((projectKey?: string) => {
    resetProjectTreeRuntimeWindowLimits(projectKey);
    setTopicWindowLimits((current) => {
      const prefix = projectKey ? `${projectKey}\u001f` : "";
      const next = projectKey
        ? Object.fromEntries(Object.entries(current).filter(([key]) => !key.startsWith(prefix)))
        : {};
      if (Object.keys(next).length === Object.keys(current).length) return current;
      topicWindowLimitsRef.current = next;
      return next;
    });
  }, []);

  const topicLoadSeqRef = useRef<Record<string, number>>({});
  const topicLoadPendingRef = useRef<Record<string, number>>({});
  const topicRequestLimiterRef = useRef(createProjectTreeRequestLimiter(4));
  const topicLoadErrorRef = useRef<Record<string, string>>({});
  const invalidateProjectTopicLists = useCallback((projectKey: string) => {
    const prefix = `${projectKey}\u001f`;
    for (const key of Object.keys(topicLoadSeqRef.current)) {
      if (key.startsWith(prefix)) topicLoadSeqRef.current[key] += 1;
    }
    for (const records of [topicLoadPendingRef.current, topicRevisionRef.current, topicCompletePageRef.current]) {
      for (const key of Object.keys(records)) if (key.startsWith(prefix)) delete records[key];
    }
    const next = Object.fromEntries(Object.entries(topicPageStateRef.current).map(([key, state]) => key.startsWith(prefix)
      ? [key, { ...state, nextCursor: undefined, loading: false, initialized: false, error: undefined }]
      : [key, state]));
    topicPageStateRef.current = next;
    setTopicPageState(next);
  }, []);

  return {
    topicRevisionRef,
    topicCompletePageRef,
    topicPageState,
    setTopicPageState,
    topicPageStateRef,
    updateTopicPageState,
    topicWindowLimits,
    setTopicWindowLimits,
    topicWindowLimitsRef,
    resetTopicWindowLimits,
    topicLoadSeqRef,
    topicLoadPendingRef,
    topicRequestLimiterRef,
    topicLoadErrorRef,
    invalidateProjectTopicLists,
  };
}
