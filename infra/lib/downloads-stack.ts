import * as cdk from "aws-cdk-lib";
import { Duration, RemovalPolicy, Stack, type StackProps } from "aws-cdk-lib";
import * as acm from "aws-cdk-lib/aws-certificatemanager";
import * as cloudfront from "aws-cdk-lib/aws-cloudfront";
import * as origins from "aws-cdk-lib/aws-cloudfront-origins";
import * as iam from "aws-cdk-lib/aws-iam";
import * as route53 from "aws-cdk-lib/aws-route53";
import * as targets from "aws-cdk-lib/aws-route53-targets";
import * as s3 from "aws-cdk-lib/aws-s3";
import { Construct } from "constructs";

type DownloadsStackProps = StackProps & {
  /** Bare registered domain, e.g. "example.sh". Its Route 53 zone must exist. */
  domainName: string;
  /** Subdomain for the download host; "" puts it on the apex. */
  recordName: string;
  /** owner/repo allowed to assume the publish role. */
  githubRepo: string;
  /**
   * The GitHub OIDC provider is one per AWS account. Leave unset to create it;
   * pass the ARN of an existing one if another stack already owns it.
   */
  githubOidcProviderArn?: string;
};

/**
 * The tennis download host: a private bucket of release archives behind
 * CloudFront, plus the role GitHub Actions assumes to publish into it.
 *
 * The bucket is private and reached only through Origin Access Control, so
 * nothing is world-readable except through the distribution — which is what
 * makes the cache headers set at upload time authoritative.
 */
export class TennisDownloadsStack extends Stack {
  constructor(scope: Construct, id: string, props: DownloadsStackProps) {
    super(scope, id, props);

    if (props.env?.region && props.env.region !== "us-east-1") {
      throw new Error(
        "Deploy tennis-downloads in us-east-1: CloudFront only accepts ACM certificates issued there.",
      );
    }

    const zone = route53.HostedZone.fromLookup(this, "HostedZone", {
      domainName: props.domainName,
    });
    const hostName = props.recordName
      ? `${props.recordName}.${props.domainName}`
      : props.domainName;

    // RETAIN because the bucket holds every published release; a stack delete
    // should not be able to break `curl | sh` for anyone who pinned a version.
    const bucket = new s3.Bucket(this, "DownloadBucket", {
      blockPublicAccess: s3.BlockPublicAccess.BLOCK_ALL,
      encryption: s3.BucketEncryption.S3_MANAGED,
      enforceSSL: true,
      removalPolicy: RemovalPolicy.RETAIN,
      versioned: true,
    });

    const certificate = new acm.Certificate(this, "Certificate", {
      domainName: hostName,
      validation: acm.CertificateValidation.fromDns(zone),
    });

    const distribution = new cloudfront.Distribution(this, "Distribution", {
      certificate,
      comment: `tennis downloads for ${hostName}`,
      defaultBehavior: {
        allowedMethods: cloudfront.AllowedMethods.ALLOW_GET_HEAD,
        // CACHING_OPTIMIZED honours the Cache-Control set at upload time, so
        // /v<version>/* stays at the edge for a year and /VERSION for minutes.
        cachePolicy: cloudfront.CachePolicy.CACHING_OPTIMIZED,
        compress: true,
        origin: origins.S3BucketOrigin.withOriginAccessControl(bucket),
        responseHeadersPolicy: cloudfront.ResponseHeadersPolicy.SECURITY_HEADERS,
        viewerProtocolPolicy: cloudfront.ViewerProtocolPolicy.REDIRECT_TO_HTTPS,
      },
      // `curl -fsSL https://<host> | sh` with no path lands on the installer.
      defaultRootObject: "install.sh",
      domainNames: [hostName],
      // A private bucket answers a missing key with 403, which reads as a
      // permissions bug. Map it to the 404 it actually is.
      errorResponses: [
        {
          httpStatus: 403,
          responseHttpStatus: 404,
          responsePagePath: "/404.txt",
          ttl: Duration.minutes(5),
        },
      ],
      minimumProtocolVersion: cloudfront.SecurityPolicyProtocol.TLS_V1_2_2021,
      priceClass: cloudfront.PriceClass.PRICE_CLASS_100,
    });

    // Alias records rather than CNAMEs, so the apex works if recordName is "".
    const aliasProps = {
      zone,
      target: route53.RecordTarget.fromAlias(
        new targets.CloudFrontTarget(distribution),
      ),
      ...(props.recordName ? { recordName: props.recordName } : {}),
    };
    new route53.ARecord(this, "AliasRecord", aliasProps);
    new route53.AaaaRecord(this, "AliasIpv6Record", aliasProps);

    // GitHub Actions authenticates by OIDC, so there is no access key to leak
    // or rotate. The provider is account-global; pass an ARN to reuse one.
    const provider = props.githubOidcProviderArn
      ? iam.OpenIdConnectProvider.fromOpenIdConnectProviderArn(
          this,
          "GitHubOidcProvider",
          props.githubOidcProviderArn,
        )
      : new iam.OpenIdConnectProvider(this, "GitHubOidcProvider", {
          url: "https://token.actions.githubusercontent.com",
          clientIds: ["sts.amazonaws.com"],
        });

    // Scoped to tag pushes: only a release can publish. A compromised PR
    // running with this repo's workflows still cannot reach the bucket.
    const publishRole = new iam.Role(this, "PublishRole", {
      assumedBy: new iam.WebIdentityPrincipal(
        provider.openIdConnectProviderArn,
        {
          StringEquals: {
            "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
          },
          StringLike: {
            "token.actions.githubusercontent.com:sub": `repo:${props.githubRepo}:ref:refs/tags/v*`,
          },
        },
      ),
      description: `Publishes tennis releases to ${hostName}`,
      maxSessionDuration: Duration.hours(1),
    });

    // Put and read, deliberately not delete: publishing overwrites the mutable
    // keys and adds new immutable ones, so a stolen token cannot erase the
    // release history that older installs still resolve against.
    bucket.grantPut(publishRole);
    bucket.grantRead(publishRole);
    publishRole.addToPolicy(
      new iam.PolicyStatement({
        actions: ["cloudfront:CreateInvalidation"],
        resources: [
          `arn:aws:cloudfront::${this.account}:distribution/${distribution.distributionId}`,
        ],
      }),
    );

    new cdk.CfnOutput(this, "DownloadBucketName", { value: bucket.bucketName });
    new cdk.CfnOutput(this, "DownloadDistributionId", {
      value: distribution.distributionId,
    });
    new cdk.CfnOutput(this, "PublishRoleArn", { value: publishRole.roleArn });
    new cdk.CfnOutput(this, "InstallCommand", {
      value: `curl -fsSL https://${hostName}/install.sh | sh`,
    });
  }
}
