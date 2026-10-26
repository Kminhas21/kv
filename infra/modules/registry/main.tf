variable "name" {
  type = string
}

variable "region" {
  type = string
}

resource "google_artifact_registry_repository" "this" {
  location      = var.region
  repository_id = var.name
  format        = "DOCKER"
}

output "url" {
  value = "${var.region}-docker.pkg.dev/${google_artifact_registry_repository.this.project}/${google_artifact_registry_repository.this.repository_id}"
}
