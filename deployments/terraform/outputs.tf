output "cluster_name" {
  description = "Kind cluster name"
  value       = var.cluster_name
}

output "kubectl_context" {
  description = "kubectl context name"
  value       = "kind-${var.cluster_name}"
}

output "registry_endpoint" {
  description = "Local image registry endpoint"
  value       = "localhost:${var.registry_port}"
}
