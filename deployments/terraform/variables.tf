variable "cluster_name" {
  description = "Kind cluster name"
  type        = string
  default     = "urlshortener"
}

variable "registry_name" {
  description = "Local registry container name"
  type        = string
  default     = "urlshortener-registry"
}

variable "registry_port" {
  description = "Host port for the local registry"
  type        = number
  default     = 5001
}
