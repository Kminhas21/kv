variable "name" {
  type        = string
  description = "Name for the VPC and its subnet."
}

variable "region" {
  type        = string
  description = "Region of the subnet."
}

variable "nodes_cidr" {
  type    = string
  default = "10.0.0.0/20"
}

variable "pods_cidr" {
  type    = string
  default = "10.4.0.0/14"
}

variable "services_cidr" {
  type    = string
  default = "10.8.0.0/20"
}

resource "google_compute_network" "this" {
  name                    = var.name
  auto_create_subnetworks = false
}

resource "google_compute_subnetwork" "this" {
  name          = var.name
  region        = var.region
  network       = google_compute_network.this.id
  ip_cidr_range = var.nodes_cidr

  # VPC-native cluster: pods and services get their own alias-IP ranges.
  secondary_ip_range {
    range_name    = "pods"
    ip_cidr_range = var.pods_cidr
  }
  secondary_ip_range {
    range_name    = "services"
    ip_cidr_range = var.services_cidr
  }
}

output "network" {
  value = google_compute_network.this.id
}

output "subnetwork" {
  value = google_compute_subnetwork.this.id
}

output "pods_range_name" {
  value = "pods"
}

output "services_range_name" {
  value = "services"
}
