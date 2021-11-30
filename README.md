## SLSA Deployment Architectures Demo

*Note: This work was presented at PackagingCon '21
([talk](https://www.youtube.com/watch?v=J5S2hSAArOk)).*

A demonstration of [SLSA](https://slsa.dev) provenance generation strategies
that don't require full build system integration.

Software engineering isn't *one size fits all* and our approach to supply chain
security shouldn't be so either. The construction presented here offers a number
of benefits:

*   Flexible deployment options
*   Easy on-boarding
*   Centralized policy storage

And while each of the strategies presented here are generalizable, the demos
target Python's packaging ecosystem for demonstration purposes.

### How to deploy

#### Prerequisites

**Services**

*   [Google Cloud account](https://console.cloud.google.com/) with
    [billing enabled](https://cloud.google.com/billing/docs/how-to/manage-billing-account)
*   [GitHub account](https://github.com/) with a
    [personal access token](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/creating-a-personal-access-token)
    with the `public_repo` scope.

**Tools**:

*   [`gcloud`](https://cloud.google.com/sdk/docs/install)
*   [`docker`](https://docs.docker.com/get-docker/)
*   [`terraform`](https://learn.hashicorp.com/tutorials/terraform/install-cli)

#### Installation

```shell
$ export GCP_PROJECT="TODO"  # GCP project ID
$ export TOKEN="ghp_TODO"    # Personal Access Token with public_repo scope
$ make images
$ export GOOGLE_OAUTH_ACCESS_TOKEN="$(gcloud auth print-access-token)"
$ terraform apply -var project="$GCP_PROJECT" -var github_token="$TOKEN" -var policy_repo="github.com/slsa-framework/provenance-architecture-demo"
```

#### Signing Policy Configuration

Signing Policies specify which provenance generation methods should be permitted
and how those generation methods should function. They can also be used by
provenance consumers to validate that the provided provenance matches the
configuration of the associated package.

Each policy must be defined in a git repo at a path following the convention
`<scope>/<package>/policy.yaml`. For example, a PyPI package called `jsonschema`
would have its policy at `pypi/jsonschema/policy.yaml`.

By default, the deployment is configured to look for policies in a github repo
specified by `-var policy_repo=""`. The available configuration parameters can
be found in [pkg/policy.go](./pkg/policy.go) and examples can be found at
[policy/](./policy/).

### Architectures

The server presented in the prototype hosts all three of the following
architectures and provides a common signing key which is publicly accessible for
validating the stored provenance. Once deployed, the public key PEM can be
fetched as follows:

```shell
$ curl  -H "Authorization: Bearer $(gcloud auth print-access-token)" \
      https://cloudkms.googleapis.com/v1/projects/$GCP_PROJECT/locations/global/keyRings/my-ring/cryptoKeys/signing-key/cryptoKeyVersions/1/publicKey \
    | jq -r .pem
```

And when provenance is generated using one of the architectures, the provenance
can be retrieved as follows:

```shell
$ curl https://<app-uri>/get?scope=pypi&pkg=idna&version=3.3
```

#### CI Monitor

The CI Monitor architecture constructs provenance from a project's existing CI
workflow. This constrasts with approaches that run inside the builds themselves
(e.g.
[`github-actions-demo`](https://github.com/slsa-framework/github-actions-demo))
in that it's isolated from the build process itself and only relies on the build
platform's integrity. This central monitor service can then be locked down and
use keys that aren't exposed to the build to attest to the provenance contents.

<img href="./docs/images/monitor.png" alt="CI Monitor Architecture" width="887" height="332" />

This prototype limits support to GitHub Actions workflows and publishes
provenance based on the data available.

The prototype implementation qualifies for L3 because the build definitions are
stored in source control and the provenance is externally-generated and
non-falsifiable. It also meets all other L3 requirements found at
slsa.dev/levels.

To find a CI build then generate and store provenance:

```shell
$ curl -X PUT https://<app-uri>/monitor?scope=pypi&pkg=jsonschema&version=4.2.1
```

#### Rebuilder

The Rebuilder architecture ingests existing artifacts, infers their likely build
process, attempts to rebuild a logically equivalent version, and, if successful,
writes provenance based on this process.

<img href="./docs/images/rebuilder.png" alt="Rebuilder Architecture" width="887" height="332" />

This prototype limits support to Python wheels and publishes the provenance if
the reconstructed wheel matches the one published on the public package index.

The prototype implementation qualifies for L2 because the provenance is
externally-generated and non-falsifiable **but** it fails to meet L3 because the
build configuration is not submitted code but rather inferred from the built
artifact. It meets all other L2 requirements found at slsa.dev/levels.

To trigger a build then generate and store provenance:

```shell
$ curl -X PUT https://<app-uri>/rebuild?scope=pypi&pkg=idna&version=3.3
```

#### Provenance Upload

The Provenance Upload architecture supports arbitrary local builds by allowing
authorized tools or users to generate and upload provenance. This scheme is
intended to be used with a sandboxed build so that build processes are decoupled
from unrelated aspects of the environment. Provenance upload is most effective
as a means of quickly onboarding build workflows that are opaque or would
otherwise require substantial work to generate signed provenance.

<img href="./docs/images/provenance_upload.png" alt="Provenance Upload Architecture" width="887" height="332" />

This prototype enforces authorization based on the identity authenticating to
the Cloud Run deployment. By default, this must be done using GCP IAM but it may
be extended to any federated or custom identity.

The prototype implementation only qualifies for L1 because no assertions can be
made as to the exact properties of the build environment, nor to the source of
the build configuration. While the provenance is signed by the central server,
it is not generated securely and thus cannot quality for higher levels as
specified at slsa.dev/levels.

To upload and store provenance:

```shell
$ curl -X PUT -H "Authorization: Bearer $(gcloud auth print-identity-token)" \
      -d "<json-provenance>" \
      https://<app-uri>/upload?scope=pypi&pkg=<package>&version=1.0
```

## Contributors

*   Matthew Suozzo

## Links

*   [SLSA Site](https://slsa.dev)
*   [PackagingCon Talk](https://www.youtube.com/watch?v=J5S2hSAArOk)
*   [SLSA Framework Repo](https://github.com/slsa-framework/slsa)
*   [SLSA Blog Post](https://security.googleblog.com/2021/06/introducing-slsa-end-to-end-framework.html)
