# tennis download host

The CDK app behind `curl -fsSL https://<host>/install.sh | sh`.

It creates a private S3 bucket of release archives, a CloudFront distribution
in front of it, an ACM certificate, the DNS records, and the IAM role GitHub
Actions assumes to publish. The marketing site and docs are untouched — they
stay on GitHub Pages.

## What ends up on the host

```text
/install.sh                                 the installer            5 min
/VERSION                                    "0.1.0"                  5 min
/manifest.json                              version + per-platform   5 min
/latest/tennis_<os>_<arch>.tar.gz           unversioned alias        5 min
/latest/tennis_<os>_<arch>.tar.gz.sha256                             5 min
/v0.1.0/tennis_0.1.0_<os>_<arch>.tar.gz     the real payload         1 year, immutable
/v0.1.0/checksums.txt                       goreleaser's checksums   1 year, immutable
```

`install.sh` reads `/VERSION`, then downloads the versioned archive and checks
it against that release's `checksums.txt`. The payload is therefore always an
immutable key: it caches at the edge forever and never needs invalidating. Only
the small pointer files are short-lived, and those are the only paths the
release workflow invalidates.

`/latest/` exists for Dockerfiles that would rather not do the lookup. Nothing
in the install path depends on it.

## One-time setup

CDK is already bootstrapped in this account for `us-east-1`, and everything
here must live in `us-east-1` — CloudFront reads ACM certificates only from
there.

**1. Register the domain and give it a Route 53 hosted zone.**

Registering through Route 53 creates the zone for you. Registering elsewhere
means creating the zone and then setting the registrar's nameservers to the
four the zone hands back:

```bash
aws route53 create-hosted-zone --name example.sh --caller-reference "$(date +%s)" \
  --query 'DelegationSet.NameServers' --output text
```

Wait for the delegation to take effect before deploying — certificate
validation fails against a zone the internet cannot see yet:

```bash
dig +short NS example.sh
```

**2. Deploy.**

`recordName` is the subdomain; pass `-c recordName=` (empty) to put the host on
the apex instead.

```bash
cd infra && npm install
AWS_REGION=us-east-1 npx cdk deploy tennis-downloads -c domainName=example.sh -c recordName=get
```

The certificate is DNS-validated against the zone in the same stack, so the
first deploy pauses a few minutes while ACM confirms it.

The stack creates the GitHub OIDC provider, which is account-global — there can
only be one. If another stack already owns it, pass it instead of creating a
second:

```bash
... -c githubOidcProviderArn=arn:aws:iam::<account>:oidc-provider/token.actions.githubusercontent.com
```

**3. Wire up the repo** with the three outputs the deploy prints.

```bash
gh variable set TENNIS_DOWNLOAD_HOST          --body get.example.sh
gh variable set TENNIS_DOWNLOAD_BUCKET        --body <DownloadBucketName>
gh variable set TENNIS_DOWNLOAD_DISTRIBUTION_ID --body <DownloadDistributionId>
gh secret   set AWS_ROLE_TO_ASSUME            --body <PublishRoleArn>
```

Until all four are set the release workflow skips publishing and cuts a GitHub
release only, so a tag never fails on missing infrastructure.

**4. Cut a release.** `git tag v0.1.1 && git push origin v0.1.1`.

The workflow tests, runs goreleaser, publishes to the bucket, invalidates the
pointer paths, then re-downloads `/VERSION` and `/install.sh` from the edge to
confirm the release is actually reachable before it goes green.

**5. Point the docs at the new command** — README.md and
`docs/content/docs/getting-started.mdx` still carry the version-pinned GitHub
URL:

```bash
curl -fsSL https://get.example.sh/install.sh | sh
```

## Notes

- The publish role is scoped to `refs/tags/v*` on `satoricorp/tennis` and can
  put and read objects but not delete them. A stolen token cannot erase the
  release history that pinned installs still resolve against.
- The bucket is versioned and set to `RETAIN`; deleting the stack leaves the
  archives in place.
- `install.sh` ships with a `TENNIS_DOWNLOAD_HOST` placeholder so the repo
  never hardcodes a domain. The workflow substitutes the real host on upload
  and fails if the placeholder survives.
- Changing the domain means a redeploy plus updating `TENNIS_DOWNLOAD_HOST`;
  the installer picks it up on the next release with no code change.
- Users can pin a version (`TENNIS_VERSION=0.1.0`), redirect the install
  directory (`TENNIS_INSTALL_DIR`), or point at another host
  (`TENNIS_INSTALL_BASE_URL`) — that last one is how the installer is tested
  against a local stand-in.
