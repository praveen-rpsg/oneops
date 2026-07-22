# Infrastructure (Terraform)

Multi-cloud IaC for OneOps. M0 ships the `dev` environment scaffold that installs
the control-plane Helm chart onto an existing Kubernetes context (e.g. a local
`kind` cluster). Cloud modules (`network`, `eks`, `rds`, `opensearch`, `s3`) are
added in M9.

## Layout

```
infra/
├── envs/
│   └── dev/          # local/dev environment (helm release)
└── modules/          # reusable cloud modules (added in M9)
```

## Usage (dev)

```bash
kind create cluster --config ../../kind-config.yaml
cd envs/dev
terraform init
terraform apply -var kube_context=kind-oneops
```
