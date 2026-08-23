"use client";

import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState } from "react";

import { NewProjectForm } from "@/app/threads/new-project-form";
import { RelativeTime } from "@/components/relative-time";
import { StatusBadge } from "@/components/status-badge";
import {
  api,
  ApiClientError,
  errorCode,
} from "@/lib/client-api";
import { threadKind } from "@/lib/status";

/**
 * Thread list. Every designed state is real: skeleton rows while loading,
 * an empty state naming the next action, typed problems with retry, and
 * rows that never lie about status.
 */
export function ThreadsView() {
  const [dialogOpen, setDialogOpen] = useState(false);

  const threadsQuery = useQuery({
    queryKey: ["threads"],
    queryFn: api.listThreads,
    refetchInterval: 5000,
  });

  const threads = threadsQuery.data?.threads ?? [];

  return (
    <div>
      <div className="page-head">
        <h1>Threads</h1>
        <button type="button" className="btn btn-primary" data-testid="new-thread" onClick={() => setDialogOpen(true)}>
          New thread
        </button>
      </div>

      {threadsQuery.isPending ? (
        <div aria-hidden="true" data-testid="threads-loading">
          <div className="skeleton-row" />
          <div className="skeleton-row" />
          <div className="skeleton-row" />
        </div>
      ) : threadsQuery.isError ? (
        <ThreadsError error={threadsQuery.error} onRetry={() => threadsQuery.refetch()} />
      ) : threads.length === 0 ? (
        <div className="state-panel card" data-testid="threads-empty">
          <p className="state-title">No threads yet</p>
          <p>
            A thread carries one outcome: describe it once, then observe its
            runs here.
          </p>
          <button type="button" className="btn btn-primary" onClick={() => setDialogOpen(true)}>
            New thread
          </button>
        </div>
      ) : (
        <ul className="thread-list stagger" data-testid="thread-rows">
          {threads.map((thread) => (
            <li key={thread.id} className="thread-row">
              <Link href={`/threads/${encodeURIComponent(thread.id)}`}>
                <span className="thread-row-title">{thread.title}</span>
                <StatusBadge label={humanStatus(thread.status)} kind={threadKind(thread.status)} />
                <span className="row-meta row-updated">
                  updated <RelativeTime at={thread.updated_at} />
                </span>
              </Link>
            </li>
          ))}
        </ul>
      )}

      <NewThreadDialog open={dialogOpen} onClose={() => setDialogOpen(false)} />
    </div>
  );
}

export function humanStatus(status: string): string {
  return status.replaceAll("_", " ");
}

function ThreadsError({ error, onRetry }: { error: unknown; onRetry: () => void }) {
  const code = errorCode(error);
  if (code === "session_expired") {
    return <ExpiredNotice />;
  }
  return (
    <div role="alert" className="state-panel card" data-testid="threads-error">
      <p className="state-title">
        {error instanceof ApiClientError ? error.problem.title : "Could not load threads"}
      </p>
      <p>
        {error instanceof ApiClientError && error.problem.detail
          ? error.problem.detail
          : "Retry the request; in-flight work is unaffected."}
      </p>
      <button type="button" className="btn" onClick={onRetry}>
        Retry
      </button>
    </div>
  );
}

export function ExpiredNotice() {
  return (
    <div className="state-panel card" data-testid="session-expired">
      <p className="state-title">Your session expired</p>
      <p>Sign in again to continue where you left off.</p>
      <a className="btn btn-primary" href="/api/auth/login?next=%2Fthreads" style={{ textDecoration: "none" }}>
        Sign in again
      </a>
    </div>
  );
}

function NewThreadDialog({
  open,
  onClose,
}: {
  open: boolean;
  onClose: () => void;
}) {
  const dialogRef = useRef<HTMLDialogElement>(null);
  const titleRef = useRef<HTMLInputElement>(null);
  const queryClient = useQueryClient();
  const router = useRouter();
  const [title, setTitle] = useState("");
  const [projectId, setProjectId] = useState("");
  const [formError, setFormError] = useState<string | undefined>();

  const projectsQuery = useQuery({
    queryKey: ["projects"],
    queryFn: api.listProjects,
    enabled: open,
  });
  const projects = projectsQuery.data?.projects ?? [];

  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (open && !dialog.open) {
      dialog.showModal();
      queueMicrotask(() => titleRef.current?.focus());
    } else if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  const createThread = useMutation({
    mutationFn: api.createThread,
    onSuccess: async (thread) => {
      setTitle("");
      setFormError(undefined);
      await queryClient.invalidateQueries({ queryKey: ["threads"] });
      onClose();
      router.push(`/threads/${encodeURIComponent(thread.id)}`);
    },
    onError: (err) => {
      if (errorCode(err) === "session_expired") {
        setFormError("Your session expired. Sign in again and retry.");
        return;
      }
      setFormError(
        err instanceof ApiClientError ? err.problem.title : "Unexpected error",
      );
    },
  });

  return (
    <dialog
      ref={dialogRef}
      onClose={onClose}
      aria-labelledby="new-thread-title"
      style={{
        border: "1px solid var(--hairline)",
        borderRadius: "var(--radius-card)",
        padding: 24,
        minWidth: 320,
        boxShadow: "var(--shadow-float)",
      }}
    >
      <h2 id="new-thread-title">New thread</h2>
      {projectsQuery.isPending ? (
        <p style={{ color: "var(--ink-2)" }}>Loading projects…</p>
      ) : projects.length === 0 ? (
        <>
          <p style={{ color: "var(--ink-2)" }}>
            A project groups threads on one repository. Create the first one:
          </p>
          <NewProjectForm
            onCreated={(project) => {
              void queryClient.invalidateQueries({ queryKey: ["projects"] });
              setProjectId(project.id);
            }}
            onError={setFormError}
          />
        </>
      ) : (
        <form
          onSubmit={(event) => {
            event.preventDefault();
            const selected = projectId || projects[0]?.id;
            if (title.trim() && selected) {
              createThread.mutate({ project_id: selected, title: title.trim() });
            }
          }}
        >
          <div style={{ margin: "16px 0 12px" }}>
            <label className="label" htmlFor="thread-project">
              Project
            </label>
            <select
              id="thread-project"
              className="select"
              value={projectId || projects[0]?.id || ""}
              onChange={(event) => setProjectId(event.target.value)}
            >
              {projects.map((project) => (
                <option key={project.id} value={project.id}>
                  {project.name}
                </option>
              ))}
            </select>
          </div>
          <div style={{ marginBottom: 16 }}>
            <label className="label" htmlFor="thread-title">
              Outcome
            </label>
            <input
              id="thread-title"
              ref={titleRef}
              className="input"
              value={title}
              placeholder="What should happen?"
              onChange={(event) => setTitle(event.target.value)}
              required
              minLength={1}
            />
          </div>
          {createThread.isError || formError ? (
            <div role="alert" className="banner banner-attention" style={{ marginBottom: 12 }}>
              <span>The thread was not created: {formError ?? "unexpected error"}.</span>
            </div>
          ) : null}
          <div style={{ display: "flex", gap: 8, justifyContent: "flex-end" }}>
            <button type="button" className="btn" onClick={onClose}>
              Cancel
            </button>
            <button
              type="submit"
              className="btn btn-primary"
              disabled={!title.trim() || createThread.isPending}
            >
              Open thread
            </button>
          </div>
        </form>
      )}
    </dialog>
  );
}
