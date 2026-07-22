provider "helm" {
  kubernetes {
    config_context = var.kube_context
  }
}

resource "helm_release" "controlplane" {
  name             = "oneops"
  namespace        = var.namespace
  create_namespace = true
  chart            = "${path.module}/../../../deploy/charts/controlplane"

  set {
    name  = "image.tag"
    value = var.image_tag
  }
}
