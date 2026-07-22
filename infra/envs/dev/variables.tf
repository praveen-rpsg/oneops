variable "kube_context" {
  description = "kubeconfig context to deploy into"
  type        = string
  default     = "kind-oneops"
}

variable "namespace" {
  description = "Kubernetes namespace for the control plane"
  type        = string
  default     = "oneops"
}

variable "image_tag" {
  description = "Control-plane image tag to deploy"
  type        = string
  default     = "0.1.0"
}
