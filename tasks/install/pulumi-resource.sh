#!/usr/bin/env bash
# Print the rest-mail resource for a Pulumi project, in that project's language.
#
#   RUNTIME=nodejs NAME=mail HOST=mail.example.com DOMAIN=example.com \
#     tasks/install/pulumi-resource.sh
#
# Called by `chore install:pulumi`, which has already put the admin password in
# the stack's config as the secret `restmail:bootstrapAdminPassword`. So the
# printed resource READS it from there and never contains it: whatever this
# prints is meant to be pasted into a file that gets committed.
#
# It prints rather than writes because a Pulumi project is a program, in a
# language and layout this repository has never seen. Editing someone's source
# is not something an installer can do safely; handing them the exact lines is.
#
# Four things in every version below were learnt by installing it, not guessed:
#   - `name` is set, or Pulumi suffixes the release (restmail-df5d9627) and every
#     object in it, so nothing is called what the documentation says.
#   - the whole oci:// reference goes in `chart`; splitting it into a repository
#     fails with a message that reads like a network problem.
#   - `skipAwait`, because every pod waits for the TLS Secret, and without it
#     `pulumi up` fails after five minutes on an install that is fine.
#     `chore install:check` is what tells you whether it is actually running.
#   - the password comes from config as a secret, so it stays encrypted in the
#     stack's state as well as out of the source.
set -euo pipefail

: "${RUNTIME:?}" "${NAME:?}" "${HOST:?}" "${DOMAIN:?}"
CHART=${CHART:-oci://ghcr.io/rest-mail/charts/restmail}
NAMESPACE=${NAMESPACE:-rest-mail}

case "$RUNTIME" in
yaml)
  cat <<EOF
Add this under \`resources:\` in Pulumi.yaml:

  $NAME:
    type: kubernetes:helm.sh/v3:Release
    properties:
      name: $NAME
      chart: $CHART
      namespace: $NAMESPACE
      createNamespace: true
      skipAwait: true
      values:
        mailserver:
          hostname: $HOST
          domain: $DOMAIN
        api:
          bootstrapAdminPassword: \${restmail:bootstrapAdminPassword}
EOF
  ;;
nodejs)
  cat <<EOF
If the project does not have it yet:

  npm install @pulumi/kubernetes

Then add this to your program (index.ts), with these imports if it does not
have them already:

  import * as pulumi from "@pulumi/pulumi";
  import * as k8s from "@pulumi/kubernetes";

const restmailConfig = new pulumi.Config("restmail");

new k8s.helm.v3.Release("$NAME", {
    name: "$NAME",
    chart: "$CHART",
    namespace: "$NAMESPACE",
    createNamespace: true,
    skipAwait: true,
    values: {
        mailserver: { hostname: "$HOST", domain: "$DOMAIN" },
        api: { bootstrapAdminPassword: restmailConfig.requireSecret("bootstrapAdminPassword") },
    },
});
EOF
  ;;
python)
  cat <<EOF
If the project does not have it yet:

  pip install pulumi-kubernetes

Then add this to your program (__main__.py), with these imports if it does
not have them already:

  import pulumi
  import pulumi_kubernetes as k8s

restmail_config = pulumi.Config("restmail")

k8s.helm.v3.Release(
    "$NAME",
    name="$NAME",
    chart="$CHART",
    namespace="$NAMESPACE",
    create_namespace=True,
    skip_await=True,
    values={
        "mailserver": {"hostname": "$HOST", "domain": "$DOMAIN"},
        "api": {"bootstrapAdminPassword": restmail_config.require_secret("bootstrapAdminPassword")},
    },
)
EOF
  ;;
go)
  cat <<EOF
If the project does not have it yet:

  go get github.com/pulumi/pulumi-kubernetes/sdk/v4

Then add this inside pulumi.Run in main.go, with these imports:

  helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
  "github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"

restmailConfig := config.New(ctx, "restmail")
if _, err := helmv3.NewRelease(ctx, "$NAME", &helmv3.ReleaseArgs{
	Name:            pulumi.String("$NAME"),
	Chart:           pulumi.String("$CHART"),
	Namespace:       pulumi.String("$NAMESPACE"),
	CreateNamespace: pulumi.Bool(true),
	SkipAwait:       pulumi.Bool(true),
	Values: pulumi.Map{
		"mailserver": pulumi.Map{"hostname": pulumi.String("$HOST"), "domain": pulumi.String("$DOMAIN")},
		"api":        pulumi.Map{"bootstrapAdminPassword": restmailConfig.RequireSecret("bootstrapAdminPassword")},
	},
}); err != nil {
	return err
}
EOF
  ;;
*)
  cat <<EOF
This project's runtime is "$RUNTIME", which this does not print for yet.
Declare a kubernetes Helm v3 Release with these properties:

  name:            $NAME
  chart:           $CHART
  namespace:       $NAMESPACE
  createNamespace: true
  skipAwait:       true
  values.mailserver.hostname:     $HOST
  values.mailserver.domain:       $DOMAIN
  values.api.bootstrapAdminPassword: the secret config value restmail:bootstrapAdminPassword
EOF
  ;;
esac
