# Repository Guidelines

## Project Structure & Module Organization

This repository is a Go workspace for a Kubernetes-based AI inference platform. Shared domain types, middleware, and storage code live in `lib/`. Deployable components are separate modules under `services/` (`controlplane`, `gateway`, `modelregistry`, `k8sadapter`, `observability`, `inference`, and `pipeline`); each service starts from `cmd/main.go` and keeps implementation details in `internal/`. The Vue 3/TypeScript console is in `frontend/`, Kubernetes resources are in `deployments/k8s/`, architecture and product decisions are in `docs/`, and development or E2E utilities are in `hack/`.

## Build, Test, and Development Commands

- `./hack/check.sh` builds, vets, and tests every Go module. Pass module paths, such as `./hack/check.sh ./services/gateway`, for a focused check.
- `./hack/dev-up.sh --storage=memory` builds and starts all six local services with the fake Kubernetes backend. Use `--storage=postgres` when persistence is required.
- `./hack/e2e-test.sh` exercises the complete fake-cluster workflow; run it after `dev-up.sh` is ready.
- `./hack/contract-test.sh` validates cross-service HTTP errors, state transitions, idempotency, and response envelopes.
- `cd frontend && npm ci && npm run dev` starts the console. `npm run build` runs TypeScript checks and creates the production bundle.

Use Go 1.26.x and Node.js 20 or newer, matching `go.work` and the README.

## Coding Style & Naming Conventions

Format Go with `gofmt`; keep packages lowercase, exported identifiers in `PascalCase`, and tests named `TestBehavior`. Preserve the existing layered layout (`cmd`, `internal/server`, `internal/biz`, `internal/data`) rather than importing another service's `internal` packages. Vue components use `PascalCase.vue`; composables use `useThing.ts`. Follow the existing TypeScript style: two-space indentation, single quotes, and no semicolons. Use two spaces in YAML.

## Testing Guidelines

Place Go unit tests beside their source as `*_test.go` and use the standard `testing` package. Add focused regression tests for changed behavior, then run `./hack/check.sh`. There is no stated coverage threshold or frontend test harness; frontend changes must at least pass `npm run build`. Run contract or E2E scripts whenever APIs, deployment state, routing, metrics, or Kubernetes rendering change.

## Commit & Pull Request Guidelines

Recent history uses only `commit` as the subject, so it provides no useful convention. Prefer short imperative subjects with an optional scope, for example `gateway: reject unauthorized models`. Keep commits focused. Pull requests should describe affected services, behavior changes, and verification commands; link relevant issues or roadmap items, and include screenshots for console changes. Never commit credentials, API keys, kubeconfigs, or local database settings.
