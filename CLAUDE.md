# Working with this repo

## Pushing to main deploys to production

`git push` to `main` fires `.github/workflows/deploy.yml` immediately — no
review step, no staging. It builds and pushes the image to
`quay.io/jmelis/birdquiz`, then pushes a manifest update to the
`jmelis/docker-compose-deployer` repo, which ArgoCD picks up and rolls out
automatically. Treat a push to `main` as a production deploy, not just a
commit: confirm with the user before pushing unless they've already asked
for it in that message.

## Docs describe current state, not the journey

When writing or updating anything in `docs/` (or other reference docs),
describe how the system works now — not the sequence of approaches tried,
rejected, or revised to get there. No "we first tried X, then realized Y,
so we switched to Z", no "an early version did A", no "this took real
back-and-forth to land on". State the current design and, where it aids
understanding, *why* it's built that way — but as present-tense
justification, not a narrated history of decisions. This is a personal
project; docs are for remembering how things work, not a decision log.

## The deployment manifest lives in `../docker-compose-deployer`

The Kubernetes manifest for this app is `k8s/applications/birdquiz/deployment.yaml`
(plus `ingress.yaml` and the sealed secret) in the sibling repo
`../docker-compose-deployer` (GitHub `jmelis/docker-compose-deployer`; the
`Makefile` and `deploy.yml` already assume that path). It's a k3s cluster
managed by ArgoCD, with auto-sync, prune and self-heal. You are expected to
edit it directly when a change to this app needs deployment config (replicas,
rollout strategy, env vars, probes, resources) — don't hand the user a snippet
to paste. Read that repo's `CLAUDE.md` first.

- **Editing is free; committing and pushing is a production deploy.** ArgoCD
  applies whatever is on `main` there immediately. Make the edit, show the
  diff, and leave it uncommitted unless the user has asked for it to be
  committed or pushed in that message. There is usually no cluster access
  from here (`kubectl` has no kubeconfig), so validate by reading, not by
  `kubectl apply`.
- **`deploy.yml` also pushes to that repo.** Each push to this repo's `main`
  makes the workflow commit an image-tag bump to the deployer's `main`. Pull
  before committing there, and don't hand-edit the `image:` tag line.
- **Never print secrets.** `k8s/applications/birdquiz/` contains a gitignored
  plaintext `birdquiz-secrets.secret.yaml`. Don't `cat` the directory or a
  `*.yaml` glob; read `deployment.yaml` by name.

### What the manifest currently relies on

- **Several replicas, rolling updates.** `replicas: 2` with `RollingUpdate`
  (`maxUnavailable: 0`, `maxSurge: 1`). That is only safe because of what the
  server does: a per-species `flock` before any Wikimedia fetch, cache files
  written via unique temp file + rename (`server/cache.go`), SQLite in WAL
  mode with a busy timeout, and cookies signed with `SESSION_SECRET` rather
  than held in memory. Don't remove any of these without revisiting the
  replica count.
- **One node.** `CACHE_DIR` and `USER_DB_PATH` are on a hostPath
  (`/srv/birdquiz.byteboa.org`), so every replica must run on that node. Don't
  add anti-affinity across nodes or a PVC without rethinking the cache locking
  (`flock` needs a filesystem shared by all pods) and SQLite.
- **Graceful shutdown.** On SIGTERM the server serves for `SHUTDOWN_DELAY`
  (5s) so Traefik drops the pod, then drains for `SHUTDOWN_TIMEOUT` (20s).
  `terminationGracePeriodSeconds: 30` must stay above the sum. If either env
  var is raised, raise that too.
- **Rate limits are per process.** `WIKIMEDIA_RPS` in the manifest is the
  total budget (8/s) divided by `replicas`. Changing `replicas` means changing
  it too; protecting the external APIs from waste is a stated priority.
- **Memory.** Each pod has a 3Gi limit and a rollout briefly runs `replicas + 1`
  pods; `hotspots.bolt` is a ~5GB mmap shared by the page cache.
- **Metrics are scraped per pod.** VictoriaMetrics scrapes the
  `birdquiz-headless` Service via `dns_sd_configs`
  (`k8s/applications/victoriametrics/configmap.yaml`), one target per replica,
  because scraping the load-balanced Service would alternate between pods'
  counters. The Grafana dashboard (`k8s/applications/grafana/dashboard-configmap.yaml`,
  `birdquiz.json`) therefore aggregates: shared-state gauges (users, favorites,
  hotspots/places loaded) use `max()`, per-process gauges and counters use
  `sum()`, and the process panels (RSS, goroutines, CPU) show one series per
  `instance`. A new panel needs the same treatment. VictoriaMetrics does not
  reload its scrape config when the ConfigMap changes; restart it
  (`kubectl rollout restart deploy/victoriametrics`) after a scrape-config edit.
