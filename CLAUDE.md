# Working with this repo

Read `ARCHITECTURE.md` first: it describes how the system is built and why
(server, data, image cache, auth, frontend, deployment, metrics). `README.md`
covers running it and refreshing its data. Design and code facts belong in
`ARCHITECTURE.md`, not here; this file is about how to work with the repo and
its environment.

## Pushing to main deploys to production

`git push` to `main` fires `.github/workflows/deploy.yml` immediately — no
review step, no staging. It builds and pushes the image to
`quay.io/jmelis/birdquiz`, then pushes a manifest update to the
`jmelis/docker-compose-deployer` repo, which ArgoCD picks up and rolls out
automatically. Treat a push to `main` as a production deploy, not just a
commit: confirm with the user before pushing unless they've already asked
for it in that message. Even a docs-only push rolls the pods.

The running version is visible at `https://birdquiz.byteboa.org/metrics`
(`birdquiz_build_info{version="<short sha>"}`), which is how to confirm a
deploy has landed.

## Docs describe current state, not the journey

When writing or updating anything in `docs/` (or other reference docs),
describe how the system works now — not the sequence of approaches tried,
rejected, or revised to get there. No "we first tried X, then realized Y,
so we switched to Z", no "an early version did A", no "this took real
back-and-forth to land on". State the current design and, where it aids
understanding, *why* it's built that way — but as present-tense
justification, not a narrated history of decisions. This is a personal
project; docs are for remembering how things work, not a decision log.

## The deployment config lives in `../docker-compose-deployer`

The Kubernetes manifests for this app are in the sibling repo
`../docker-compose-deployer` (GitHub `jmelis/docker-compose-deployer`; the
`Makefile` and `deploy.yml` already assume that path): `k8s/applications/birdquiz/`
(deployment, ingress, sealed secret), and the monitoring that watches it in
`k8s/applications/victoriametrics/configmap.yaml` (scrape config) and
`k8s/applications/grafana/dashboard-configmap.yaml` (`birdquiz.json`). It's a
k3s cluster managed by ArgoCD with auto-sync, prune and self-heal. Edit these
files directly when a change needs deployment config (replicas, strategy, env
vars, probes, resources, scrape targets, dashboards) — don't hand the user a
snippet to paste. Read that repo's `CLAUDE.md` first.

- **Editing is free; committing and pushing there is a production change.**
  ArgoCD applies whatever is on `main` immediately. Make the edit, show the
  diff, and leave it uncommitted unless the user has asked for it to be
  committed or pushed in that message.
- **Order matters when a manifest change depends on new server code.** Push
  and deploy this repo first, confirm the new version is live, then pull and
  push the manifest change (see "Rollouts and replicas" in `ARCHITECTURE.md`
  for what currently depends on what).
- **`deploy.yml` also pushes to that repo.** Each push to this repo's `main`
  makes the workflow commit an image-tag bump to the deployer's `main`. Pull
  (`git pull --rebase --autostash`) before committing there, and don't
  hand-edit the `image:` tag line.
- **You have `kubectl` access; use it.** The kubeconfig is
  `../docker-compose-deployer/.kube/config-zooloo` (that repo's `.envrc`
  exports it, but a shell started here doesn't load it), so prefix commands
  with `KUBECONFIG=/Users/jmelis/personal/git/docker-compose-deployer/.kube/config-zooloo`
  or `export` it in the same command. Without it `kubectl` falls back to
  `localhost:8080` and fails. Never read or print the kubeconfig file itself.
  Everything is in the `default` namespace; the app is `app=birdquiz`, a
  single node (`zooloo2`).
  - Read-only inspection is fine without asking: `get pods/endpoints/cm`,
    `logs`, `describe`, `rollout status`, `port-forward` plus a local query.
  - Verify a change actually took effect rather than assuming ArgoCD synced
    it, e.g. `kubectl get pods -l app=birdquiz`, or query VictoriaMetrics via
    `kubectl port-forward deploy/victoriametrics 18428:8428` and
    `curl localhost:18428/api/v1/query?query=up{job="birdquiz"}` (also
    `/api/v1/targets`). Stop the port-forward afterwards.
  - Changing the cluster by hand (`apply`, `edit`, `delete`, `scale`) is
    reverted by ArgoCD self-heal and is a production change: change the
    manifests in git instead. `kubectl rollout restart` is the exception that
    is sometimes needed, because VictoriaMetrics doesn't reload its scrape
    config on its own; do it when the user has asked for the config change
    to be applied, and say so.
  - Timing: after a push, ArgoCD syncs within a few minutes. A restart that
    races the sync loads the old config, so check the ConfigMap content in
    the cluster before restarting a pod to pick it up.
- **Never print secrets.** `k8s/applications/birdquiz/` contains a gitignored
  plaintext `birdquiz-secrets.secret.yaml`. Don't `cat` the directory or a
  `*.yaml` glob; read `deployment.yaml` by name.
