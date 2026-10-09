# Repo-scoped Workflows under Subspace home

Workflow YAML and WorkflowRun artifacts live under Subspace home, namespaced by Repo (`~/.local/subspace/<repo>/workflows/*.yaml` and `~/.local/subspace/<repo>/<run-id>/`). Operators register Repos in `repos.json` (name → absolute working-tree path) via CLI; the Engine resolves cwd from that registry. The SPA home page fans out over `GET /api/repos` plus nested per-Repo list APIs rather than a flat aggregate directory endpoint. This replaces monorepo-global definitions so each Repo can tune its own Workflows.

Step Attachments live beside their definition at `~/.local/subspace/<repo>/workflows/<workflow>/<step>/<file>`, not directly under `<repo>/`, so Workflow ids can never collide with `workflows/` or run-id directories.
