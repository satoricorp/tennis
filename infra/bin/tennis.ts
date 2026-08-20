#!/usr/bin/env node
import * as cdk from "aws-cdk-lib";
import { TennisDownloadsStack } from "../lib/downloads-stack.js";

const app = new cdk.App();

const domainName = app.node.tryGetContext("domainName");
if (!domainName || typeof domainName !== "string") {
  throw new Error("Pass -c domainName=<domain> when running cdk");
}

// "" puts the download host on the apex: https://<domain>/install.sh
const recordName = app.node.tryGetContext("recordName") ?? "get";
const githubRepo = app.node.tryGetContext("githubRepo") ?? "satoricorp/tennis";
const githubOidcProviderArn = app.node.tryGetContext("githubOidcProviderArn");

new TennisDownloadsStack(app, "tennis-downloads", {
  // us-east-1 is not a preference: CloudFront reads ACM certificates only
  // from there. HostedZone.fromLookup also needs a concrete account/region.
  env: { account: process.env.CDK_DEFAULT_ACCOUNT, region: "us-east-1" },
  domainName,
  recordName,
  githubRepo,
  githubOidcProviderArn,
});
