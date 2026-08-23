"use client";

import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";

import { api, ApiClientError, errorCode } from "@/lib/client-api";

/**
 * First-project form. The tenant itself is bootstrapped automatically at
 * first login (documented open endpoint); a project is the first thing a
 * human deliberately creates on top of it.
 */
/**
 * Today's /v1 seeds every project from the single registered repository
 * fixture (the embedded calc-demo; see internal/app/seeder.go). The field
 * stays explicit here so the console never pretends to offer repository
 * connections that do not exist yet — remote SCM replaces this in a later
 * wave.
 */
const REGISTERED_SEED = "calc-demo";

export function NewProjectForm({
  onCreated,
  onError,
}: {
  onCreated: (project: { id: string; name: string }) => void;
  onError?: (message: string | undefined) => void;
}) {
  const queryClient = useQueryClient();
  const [slug, setSlug] = useState("");
  const [name, setName] = useState("");

  const createProject = useMutation({
    mutationFn: api.createProject,
    onSuccess: async (project) => {
      setSlug("");
      setName("");
      await queryClient.invalidateQueries({ queryKey: ["projects"] });
      onCreated(project);
    },
    onError: (err) => {
      if (errorCode(err) === "session_expired") {
        onError?.("Your session expired. Sign in again and retry.");
        return;
      }
      onError?.(
        err instanceof ApiClientError ? err.problem.title : "Unexpected error",
      );
    },
  });

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault();
        if (!slug.trim() || !name.trim()) return;
        createProject.mutate({
          slug: slug.trim(),
          name: name.trim(),
          default_branch: "main",
          seed_name: REGISTERED_SEED,
        });
      }}
    >
      <div style={{ margin: "12px 0" }}>
        <label className="label" htmlFor="project-name">
          Project name
        </label>
        <input
          id="project-name"
          className="input"
          value={name}
          placeholder="Payments service"
          onChange={(event) => setName(event.target.value)}
          required
        />
      </div>
      <div style={{ marginBottom: 16 }}>
        <label className="label" htmlFor="project-slug">
          Slug
        </label>
        <input
          id="project-slug"
          className="input mono"
          value={slug}
          placeholder="payments-service"
          pattern="[a-z0-9][a-z0-9-]*"
          onChange={(event) => setSlug(event.target.value.toLowerCase())}
          required
        />
      </div>
      {createProject.isError && !onError ? (
        <div role="alert" className="banner banner-attention" style={{ marginBottom: 12 }}>
          <span>The project was not created. Adjust the input and retry.</span>
        </div>
      ) : null}
      <button type="submit" className="btn btn-primary" disabled={createProject.isPending}>
        Create project
      </button>
    </form>
  );
}
