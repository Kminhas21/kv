terraform {
  required_version = ">= 1.5"

  required_providers {
    google = {
      source  = "hashicorp/google"
      version = "~> 6.0"
    }
  }

  # Partial config: pass the bucket at init time (see infra/README.md).
  backend "gcs" {
    prefix = "vmaas/dev"
  }
}

variable "project_id" {
  type        = string
  description = "GCP project that owns everything."
}

variable "region" {
  type    = string
  default = "us-central1"
}

provider "google" {
  project = var.project_id
  region  = var.region
}

module "network" {
  source = "../../modules/network"
  name   = "vmaas"
  region = var.region
}

module "gke" {
  source              = "../../modules/gke"
  name                = "vmaas"
  region              = var.region
  network             = module.network.network
  subnetwork          = module.network.subnetwork
  pods_range_name     = module.network.pods_range_name
  services_range_name = module.network.services_range_name
}

module "registry" {
  source = "../../modules/registry"
  name   = "vmaas"
  region = var.region
}

output "registry_url" {
  value = module.registry.url
}

output "cluster_name" {
  value = module.gke.name
}
