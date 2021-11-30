variable "project" {
  type = string
}
variable "github_token" {
  type = string
}
variable "policy_repo" {
  type = string
  validation {
    error_message = "Malformed policy_repo. Must be of the form 'github.com/owner/name'."
    condition = can(regex("github.com/(?P<owner>[^/]+)/(?P<name>[^/]+)", var.policy_repo))
  }
}

provider "google" {
  project = "${var.project}"
  region  = "us-central1"
  zone    = "us-central1-c"
}
resource "google_project_service" "run" {
  service = "run.googleapis.com"
}
resource "google_cloud_run_service" "default" {
  name     = "slsa-provenance-server"
  location = "us-central1"
  template {
    spec {
      containers {
        image = "gcr.io/${var.project}/server"
        args = [
          "--project=${var.project}",
          "--github_token=${var.github_token}",
          "--kms_key=${google_kms_crypto_key.signing_key.id}/cryptoKeyVersions/1",
          "--policy_repo_owner=${regex("github.com/(?P<owner>[^/]+)/(?P<name>[^/]+)", var.policy_repo).owner}",
          "--policy_repo_name=${regex("github.com/(?P<owner>[^/]+)/(?P<name>[^/]+)", var.policy_repo).name}",
          "--policy_repo_dir=policy"
        ]
      }
    }
  }
  traffic {
    percent         = 100
    latest_revision = true
  }
  depends_on = [google_project_service.run]
}
resource "google_project_service" "compute" {
  service = "compute.googleapis.com"
}
data "google_compute_default_service_account" "default" {
  depends_on = [google_project_service.compute]
}
resource "google_project_service" "kms" {
  service = "cloudkms.googleapis.com"
}
resource "google_kms_key_ring" "keyring" {
  name     = "my-ring"
  location = "global"
  depends_on = [google_project_service.kms]
}
resource "google_kms_crypto_key" "signing_key" {
  name     = "signing-key"
  key_ring = google_kms_key_ring.keyring.id
  purpose  = "ASYMMETRIC_SIGN"
  version_template {
      algorithm = "EC_SIGN_P256_SHA256"
  }
  lifecycle {
      prevent_destroy = true
  }
}
resource "google_kms_crypto_key_iam_member" "signing_key_signer" {
  crypto_key_id = google_kms_crypto_key.signing_key.id
  role          = "roles/cloudkms.signer"
  member        = "serviceAccount:${data.google_compute_default_service_account.default.email}"
}
resource "google_kms_crypto_key_iam_member" "signing_key_viewer" {
  crypto_key_id = google_kms_crypto_key.signing_key.id
  role          = "roles/cloudkms.publicKeyViewer"
  member        = "allUsers"
}
resource "google_project_service" "gae" {
  service = "appengine.googleapis.com"
}
# NOTE: Use side-effect to create firestore db.
resource "google_app_engine_application" "dummy_app" {
  project       = var.project
  location_id   = "us-central"
  database_type = "CLOUD_FIRESTORE"
  depends_on = [google_project_service.gae]
}
