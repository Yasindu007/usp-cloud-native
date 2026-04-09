terraform {
  required_version = ">= 1.7"

  required_providers {
    null = {
      source  = "hashicorp/null"
      version = "~> 3.2"
    }
  }

  backend "local" {
    path = "terraform.tfstate"
  }
}

locals {
  cluster_config = "${path.module}/../kind/cluster.yaml"
}

resource "null_resource" "local_registry" {
  triggers = {
    registry_name = var.registry_name
    registry_port = tostring(var.registry_port)
  }

  provisioner "local-exec" {
    interpreter = ["PowerShell", "-Command"]
    command     = <<-EOT
      $exists = docker ps -a --filter "name=^/${var.registry_name}$" --format "{{.Names}}"
      if (-not $exists) {
        docker run -d --restart=always -p ${var.registry_port}:5000 --name ${var.registry_name} registry:2 | Out-Null
      } else {
        docker start ${var.registry_name} | Out-Null
      }
    EOT
  }
}

resource "null_resource" "kind_cluster" {
  triggers = {
    cluster_name = var.cluster_name
    config_hash  = filesha256(local.cluster_config)
  }

  provisioner "local-exec" {
    interpreter = ["PowerShell", "-Command"]
    command     = <<-EOT
      $clusters = kind get clusters
      if ($clusters -notcontains "${var.cluster_name}") {
        kind create cluster --name ${var.cluster_name} --config "${local.cluster_config}"
      } else {
        kind export kubeconfig --name ${var.cluster_name}
      }
    EOT
  }

  depends_on = [null_resource.local_registry]
}

resource "null_resource" "registry_network" {
  triggers = {
    cluster_name  = var.cluster_name
    registry_name = var.registry_name
    registry_port = tostring(var.registry_port)
  }

  provisioner "local-exec" {
    interpreter = ["PowerShell", "-Command"]
    command     = <<-EOT
      docker network connect kind ${var.registry_name} 2>$null
      if ($LASTEXITCODE -ne 0) {
        $LASTEXITCODE = 0
      }
      kind export kubeconfig --name ${var.cluster_name}
      @"
apiVersion: v1
kind: ConfigMap
metadata:
  name: local-registry-hosting
  namespace: kube-public
data:
  localRegistryHosting.v1: |
    host: "localhost:${var.registry_port}"
    help: "https://kind.sigs.k8s.io/docs/user/local-registry/"
"@ | kubectl apply -f -
    EOT
  }

  depends_on = [null_resource.kind_cluster]
}
