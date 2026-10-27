#!/usr/bin/env bash
# The rest-mail resource for a Pulumi project, in that project's language, and
# where it goes: a file of its own, plus the one line that wires it in.
#
#   RUNTIME=nodejs NAME=restmail tasks/lifecycle/pulumi-code.sh
#
# Every fact comes from the stack's config under NAME — hostname, domain,
# namespace, chartVersion, adminPasswordHash — which install:pulumi sets. So
# the code is the same for every install and every stack, contains no secret,
# and upgrade:pulumi never has to touch it: an upgrade is a config change. The
# admin password is there only as a bcrypt hash, which nobody can sign in with,
# so it is an ordinary value. Optional APNs credentials use encrypted config.
#
# It prints rather than writes. A Pulumi project is a program, in a layout this
# repository has never seen; the wiring line is an edit to it either way, and
# an installer has no business making that edit.
#
# Learnt by installing it, not guessed:
#   - `name` is set, or Pulumi suffixes the release (restmail-df5d9627) and
#     every object in it, and nothing is called what the tasks look for.
#   - the whole oci:// reference goes in `chart`; split into a repository it
#     fails with a message that reads like a network problem.
#   - `skipAwait`, because every pod waits for the TLS Secret, and without it
#     `pulumi up` fails after five minutes on an install that is fine.
#     server:verify is what says whether it is really running.
#   - a Release, not a helm.v3.Chart: a Release uninstalls through Helm, which
#     honours the chart's resource-policy: keep, so removing it keeps the mail.
set -euo pipefail

: "${RUNTIME:?}" "${NAME:?}"
CHART=${CHART:-oci://ghcr.io/rest-mail/charts/restmail}
module=${NAME//-/_}

case "$RUNTIME" in
yaml)
  cat <<EOF
Add this under \`resources:\` in Pulumi.yaml:

  $module:
    type: kubernetes:helm.sh/v3:Release
    properties:
      name: $NAME
      chart: $CHART
      version: \${$NAME:chartVersion}
      namespace: \${$NAME:namespace}
      createNamespace: true
      skipAwait: true
      values:
        mailserver:
          hostname: \${$NAME:hostname}
          domain: \${$NAME:domain}
        api:
          bootstrapAdminPasswordHash: \${$NAME:adminPasswordHash}
EOF
  # YAML has no optional Config.get: only reference a key the installer set.
  if [ -n "${OWNER_KEYS_CONFIGURED:-}" ]; then
    printf '          ownerKeys: ${%s:ownerKeys}\n' "$NAME"
  fi
  ;;
nodejs)
  component_dir=$(cd "$(dirname "$0")/../../packages/pulumi" && pwd)
  package_version=$(jq -r .version "$component_dir/package.json")
  project_dir=${PROJECT:-/path/to/your/project}
  printf '1. Build the component from this checkout and install its local artifact (Node.js 22+):\n\n'
  printf '     mkdir -p %q\n' "$project_dir/vendor"
  printf '     (cd %q && npm ci --ignore-scripts && npm pack --pack-destination %q)\n' "$component_dir" "$project_dir/vendor"
  printf '     (cd %q && npm install --save-exact %q)\n\n' "$project_dir" "./vendor/rest-mail-pulumi-$package_version.tgz"
  cat <<EOF
2. Make a file $module.ts:

import * as pulumi from "@pulumi/pulumi";
import * as k8s from "@pulumi/kubernetes";
import { RestMail } from "@rest-mail/pulumi";

export function create_$module(provider: k8s.Provider): RestMail {
    const mail = new pulumi.Config("$NAME");
    const apnsPrivateKey = mail.getSecret("apnsPrivateKey");
    return new RestMail("$NAME", {
        hostname: mail.require("hostname"),
        domain: mail.require("domain"),
        namespace: mail.require("namespace"),
        createNamespace: mail.getBoolean("createNamespace") ?? true,
        releaseName: "$NAME",
        chartVersion: mail.require("chartVersion"),
        chart: "$CHART",
        adminPasswordHash: mail.require("adminPasswordHash"),
        ownerKeys: mail.get("ownerKeys") || "",
        apns: apnsPrivateKey === undefined ? undefined : {
            privateKey: apnsPrivateKey,
            keyId: mail.require("apnsKeyId"),
            teamId: mail.require("apnsTeamId"),
            topic: mail.get("apnsTopic") || "com.antimatterstudios.restmail",
        },
    }, { providers: { kubernetes: provider } });
}

3. Wire it in, in index.ts, passing your existing Kubernetes provider
   (called cluster in homelab-server):

     import { create_$module } from "./$module";
     const mail = create_$module(cluster);
     export const mailUrl = mail.url;

The vendor artifact and package manifests can be committed with your program.
No npm publication or pipeline change is performed by this installer.
EOF
  ;;
python)
  cat <<EOF
1. Add the Kubernetes provider, if the project does not have it:

     pip install pulumi-kubernetes      (and add it to requirements.txt)

2. Make a file $module.py:

import pulumi
import pulumi_kubernetes as k8s

mail = pulumi.Config("$NAME")

release = k8s.helm.v3.Release(
    "$NAME",
    name="$NAME",
    chart="$CHART",
    version=mail.require("chartVersion"),
    namespace=mail.require("namespace"),
    create_namespace=True,
    skip_await=True,
    values={
        "mailserver": {"hostname": mail.require("hostname"), "domain": mail.require("domain")},
        "api": {"bootstrapAdminPasswordHash": mail.require("adminPasswordHash"), "ownerKeys": mail.get("ownerKeys") or ""},
    },
)

3. Wire it in, in __main__.py:

     import $module
EOF
  ;;
go)
  fn="install${module^}"
  cat <<EOF
1. Add the Kubernetes provider, if the project does not have it:

     go get github.com/pulumi/pulumi-kubernetes/sdk/v4

2. Make a file $module.go:

package main

import (
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func $fn(ctx *pulumi.Context) error {
	mail := config.New(ctx, "$NAME")
	_, err := helmv3.NewRelease(ctx, "$NAME", &helmv3.ReleaseArgs{
		Name:            pulumi.String("$NAME"),
		Chart:           pulumi.String("$CHART"),
		Version:         pulumi.String(mail.Require("chartVersion")),
		Namespace:       pulumi.String(mail.Require("namespace")),
		CreateNamespace: pulumi.Bool(true),
		SkipAwait:       pulumi.Bool(true),
		Values: pulumi.Map{
			"mailserver": pulumi.Map{"hostname": pulumi.String(mail.Require("hostname")), "domain": pulumi.String(mail.Require("domain"))},
			"api":        pulumi.Map{"bootstrapAdminPasswordHash": pulumi.String(mail.Require("adminPasswordHash")), "ownerKeys": pulumi.String(mail.Get("ownerKeys"))},
		},
	})
	return err
}

3. Wire it in, inside pulumi.Run in main.go:

     if err := $fn(ctx); err != nil {
         return err
     }
EOF
  ;;
*)
  cat <<EOF
This project's runtime is "$RUNTIME", which this does not print for yet.
Declare a kubernetes:helm.sh/v3:Release (a Release, not a Chart) with:

  name:            $NAME
  chart:           $CHART
  version:         config $NAME:chartVersion
  namespace:       config $NAME:namespace
  createNamespace: true
  skipAwait:       true
  values.mailserver.hostname:        config $NAME:hostname
  values.mailserver.domain:          config $NAME:domain
  values.api.bootstrapAdminPasswordHash: config $NAME:adminPasswordHash
  values.api.ownerKeys:                 optional config $NAME:ownerKeys (public keys only)
EOF
  ;;
esac
