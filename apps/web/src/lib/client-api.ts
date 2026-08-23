import type { Problem } from "@/lib/problem";
import { collectRunHistory } from "@/lib/runs";
import type {
  Event,
  Message,
  Project,
  Run,
  RunPage,
  RunReport,
  Task,
  Thread,
} from "@ants/contracts";

/**
 * Browser-side access to the Ants API, exclusively through the BFF. The
 * browser never sees credentials: every call rides the session cookie, and
 * correlation ids are minted here so they continue through BFF into /v1.
 */

const IDEMPOTENCY_KEY_HEADER = "Idempotency-Key";

/** One fresh correlation id per logical request attempt. */
function requestId(): string {
  return `web_${crypto.randomUUID()}`;
}

export class ApiClientError extends Error {
  readonly status: number;
  readonly problem: Problem;
  /** Present when the API answered 429 with a Retry-After hint (seconds). */
  readonly retryAfterSeconds?: number;
  constructor(status: number, problem: Problem, retryAfterSeconds?: number) {
    super(problem.detail || problem.title || problem.code);
    this.name = "ApiClientError";
    this.status = status;
    this.problem = problem;
    this.retryAfterSeconds = retryAfterSeconds;
  }
  get code(): string {
    return this.problem.code;
  }
}

export function errorCode(err: unknown): string | undefined {
  return err instanceof ApiClientError ? err.code : undefined;
}

type RequestOptions = {
  method?: "GET" | "POST" | "DELETE";
  body?: unknown;
  /** Stable across retries of one logical mutation; forwarded verbatim. */
  idempotencyKey?: string;
};

async function request<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const headers: Record<string, string> = { "x-request-id": requestId() };
  if (options.body !== undefined) headers["content-type"] = "application/json";
  if (options.idempotencyKey) headers[IDEMPOTENCY_KEY_HEADER] = options.idempotencyKey;

  let response: Response;
  try {
    response = await fetch(`/api/v1/${path}`, {
      method: options.method ?? "GET",
      headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body),
      // Session cookie flows automatically; never cache operational data.
      cache: "no-store",
    });
  } catch {
    // Network-level failure (offline, refused): classified for the UI.
    throw new ApiClientError(0, {
      type: "about:blank",
      code: "network_failure",
      title: "Network unreachable",
      status: 0,
      detail: "The request never left the browser. Check the connection.",
    });
  }

  if (!response.ok) {
    let problem: Problem;
    try {
      const body = (await response.json()) as Partial<Problem>;
      problem =
        body && typeof body.code === "string"
          ? ({ ...body, status: response.status } as Problem)
          : unreadable(response.status);
    } catch {
      problem = unreadable(response.status);
    }
    throw new ApiClientError(
      response.status,
      problem,
      parseRetryAfter(response.headers.get("retry-after")),
    );
  }
  if (response.status === 204) return undefined as T;
  return (await response.json()) as T;
}

function unreadable(status: number): Problem {
  return {
    type: "about:blank",
    code: "unexpected_response",
    title: "Unreadable error from the console backend",
    status,
  };
}

function parseRetryAfter(raw: string | null): number | undefined {
  if (!raw) return undefined;
  const seconds = Number(raw);
  return Number.isInteger(seconds) && seconds >= 0 ? seconds : undefined;
}

// ---- typed surface over the generated contract ----

export type ProjectListResponse = { projects: Project[] };
export type ThreadListResponse = { threads: Thread[] };
export type MessagePageResponse = { messages: Message[]; total: number };
export type EventPageResponse = { events: Event[] };
export type RunPageResponse = RunPage;
export type RunWithTasksResponse = { run: Run; tasks: Task[] };

export const api = {
  listProjects: () => request<ProjectListResponse>("projects"),
  createProject: (body: { slug: string; name: string; default_branch: string; seed_name: string }) =>
    request<Project>("projects", { method: "POST", body }),
  listThreads: () => request<ThreadListResponse>("threads"),
  createThread: (body: { project_id: string; title: string }) =>
    request<Thread>("threads", { method: "POST", body }),
  getThread: (id: string) => request<Thread>(`threads/${encodeURIComponent(id)}`),
  listMessages: (threadId: string, after: number) =>
    request<MessagePageResponse>(
      `threads/${encodeURIComponent(threadId)}/messages?after=${after}`,
    ),
  appendMessage: (threadId: string, content: string) =>
    request<Message>(`threads/${encodeURIComponent(threadId)}/messages`, {
      method: "POST",
      body: { content },
    }),
  // The /v1 contract requires an Idempotency-Key exactly where replay
  // protection matters today (start-run); the BFF forwards it verbatim.
  startRun: (threadId: string, key: string) =>
    request<Run>(`threads/${encodeURIComponent(threadId)}/runs`, { method: "POST", idempotencyKey: key }),
  // Keyset pages in the store-assigned per-thread sequence order (true
  // creation order); `after` is a seq value. The newest run is the last
  // element of the FINAL page, so consumers wanting it must walk to the end
  // via listAllThreadRuns.
  listThreadRuns: (threadId: string, after: number) =>
    request<RunPageResponse>(
      `threads/${encodeURIComponent(threadId)}/runs?after=${after}`,
    ),
  getRunWithTasks: (runId: string) =>
    request<RunWithTasksResponse>(`runs/${encodeURIComponent(runId)}`),
  listRunEvents: (runId: string, after: number) =>
    request<EventPageResponse>(`runs/${encodeURIComponent(runId)}/events?after=${after}`),
  cancelRun: (runId: string) =>
    request<{ status: string }>(`runs/${encodeURIComponent(runId)}/cancel`, { method: "POST" }),
  getRunReport: (runId: string) => request<RunReport>(`runs/${encodeURIComponent(runId)}/report`),
};

/**
 * The thread's complete run history, consumed page by page until the
 * authoritative `total` is exhausted; the last item of the returned list is
 * the true latest run however long the history is. Each page resumes at the
 * last run's store-assigned sequence, so concurrent starts (whatever their
 * timestamps say) can only extend the tail. One call per server page,
 * strictly sequential — callers get the whole history as a single promise,
 * so there is no client-side waterfall and no way for a render to observe a
 * partially walked history.
 */
export function listAllThreadRuns(threadId: string): Promise<RunPage> {
  const id = encodeURIComponent(threadId);
  return collectRunHistory((after) =>
    request<RunPage>(`threads/${id}/runs?after=${after}`),
  );
}
