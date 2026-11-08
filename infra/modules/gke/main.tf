variable "name" {
  type = string
}

variable "region" {
  type = string
}

variable "network" {
  type = string
}

variable "subnetwork" {
  type = string
}

variable "pods_range_name" {
  type = string
}

variable "services_range_name" {
  type = string
}

resource "google_container_cluster" "this" {
  name     = var.name
  location = var.region

  enable_autopilot = true # Google manages nodes; we pay per pod request.

  network    = var.network
  subnetwork = var.subnetwork
  ip_allocation_policy {
    cluster_secondary_range_name  = var.pods_range_name
    services_secondary_range_name = var.services_range_name
  }

  release_channel {
    channel = "REGULAR"
  }

  # Learning cluster: must be destroyable with `terraform destroy`.
  deletion_protection = false
}

output "name" {
  value = google_container_cluster.this.name
}

output "endpoint" {
  value = google_container_cluster.this.endpoint
}
