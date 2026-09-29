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
