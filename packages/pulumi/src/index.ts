import { createHash } from "node:crypto";
import * as pulumi from "@pulumi/pulumi";
import * as k8s from "@pulumi/kubernetes";

export type ApnsArgs = {
    keyId: pulumi.Input<string>;
    teamId: pulumi.Input<string>;
    /** Defaults to the REST Mail iOS app's bundle identifier. */
    topic?: pulumi.Input<string>;
} & (
    | {
          /** PEM contents of Apple's APNs .p8 key, normally Config.requireSecret(). */
          privateKey: pulumi.Input<string>;
          existingSecret?: never;
          secretKey?: never;
      }
    | {
          /** An existing Secret in this installation's namespace. */
          existingSecret: pulumi.Input<string>;
          secretKey?: pulumi.Input<string>;
          privateKey?: never;
      }
);

export interface RestMailArgs {
    hostname: pulumi.Input<string>;
    domain: pulumi.Input<string>;
    /** An explicit chart version; no implicit upgrade to latest. */
    chartVersion: pulumi.Input<string>;
    /** Optional chart reference; defaults to the published REST Mail chart. */
    chart?: pulumi.Input<string>;
    adminPasswordHash: pulumi.Input<string>;
    ownerKeys?: pulumi.Input<string>;
    /** Defaults to the component's name. */
    namespace?: pulumi.Input<string>;
    /** False when the namespace is managed elsewhere. Defaults to true. */
    createNamespace?: boolean;
    /** Defaults to the component's name, without a random suffix. */
    releaseName?: string;
    apns?: ApnsArgs;
    /** Additional chart values. Typed component arguments take precedence. */
    values?: k8s.helm.v3.ReleaseArgs["values"];
}

/** A REST Mail Helm installation, with optional managed APNs credentials. */
export class RestMail extends pulumi.ComponentResource {
    public readonly namespace?: k8s.core.v1.Namespace;
    public readonly namespaceName: pulumi.Output<string>;
    public readonly apnsSecret?: k8s.core.v1.Secret;
    public readonly release: k8s.helm.v3.Release;
    public readonly url: pulumi.Output<string>;

    constructor(name: string, args: RestMailArgs, opts?: pulumi.ComponentResourceOptions) {
        const releaseName = args.releaseName ?? name;
        if (releaseName.length > 53 || !/^[a-z0-9]([-a-z0-9]*[a-z0-9])?$/.test(releaseName)) {
            throw new Error("REST Mail releaseName must be a lowercase Helm name of at most 53 characters");
        }
        if (args.apns?.privateKey !== undefined && args.apns.existingSecret !== undefined) {
            throw new Error("APNs accepts privateKey or existingSecret, never both");
        }

        if (args.apns && args.apns.privateKey === undefined && args.apns.existingSecret === undefined) {
            throw new Error("APNs needs privateKey or existingSecret");
        }
        super("restmail:deployment:RestMail", name, {}, opts);

        const namespace = args.namespace ?? name;
        if (args.createNamespace !== false) {
            this.namespace = new k8s.core.v1.Namespace(`${name}-namespace`, {
                metadata: { name: namespace },
            }, {
                parent: this,
                // Helm keeps mail PVCs on uninstall. Deleting their namespace
                // would delete them anyway, so leave the namespace in place.
                retainOnDelete: true,
            });
            this.namespaceName = this.namespace.metadata.apply(metadata => metadata.name!);
        } else {
            this.namespaceName = pulumi.output(namespace);
        }

        const apns = args.apns;
        let apnsValues: pulumi.Input<Record<string, pulumi.Input<unknown>>> = { enabled: false };
        if (apns) {
            let secretName: pulumi.Input<string>;
            if (apns.privateKey !== undefined) {
                // Force secret propagation even when a caller supplies a plain
                // string instead of an already-secret Pulumi Output.
                const key = pulumi.secret(apns.privateKey).apply(value => {
                    if (!value.trim()) throw new Error("APNs privateKey must not be empty");
                    return value;
                });
                const keyName = key.apply(value => {
                    const digest = createHash("sha256").update(value).digest("hex").slice(0, 16);
                    return `${releaseName}-apns-${digest}`;
                });
                this.apnsSecret = new k8s.core.v1.Secret(`${name}-apns`, {
                    metadata: { name: keyName, namespace: this.namespaceName },
                    type: "Opaque",
                    stringData: { "AuthKey.p8": key },
                }, {
                    parent: this,
                    additionalSecretOutputs: ["data", "stringData"],
                });
                // A new key gives the Secret a new name. The chart's changed
                // volume reference rolls the API, which reads its key at start.
                secretName = this.apnsSecret.metadata.apply(metadata => metadata.name!);
            } else {
                if (apns.existingSecret === undefined) {
                    throw new Error("APNs needs privateKey or existingSecret");
                }
                secretName = apns.existingSecret;
            }
            apnsValues = {
                enabled: true,
                existingSecret: secretName,
                secretKey: apns.secretKey ?? "AuthKey.p8",
                keyId: apns.keyId,
                teamId: apns.teamId,
                topic: apns.topic ?? "com.antimatterstudios.restmail",
            };
        }

        const values = pulumi.output(args.values ?? {}).apply(extraValues => {
            const extra = extraValues ?? {};
            return {
                ...extra,
                mailserver: {
                    ...extra.mailserver,
                    hostname: args.hostname,
                    domain: args.domain,
                },
                api: {
                    ...extra.api,
                    bootstrapAdminPasswordHash: args.adminPasswordHash,
                    ownerKeys: args.ownerKeys ?? "",
                    push: { ...extra.api?.push, apns: apnsValues },
                },
            };
        });
        this.release = new k8s.helm.v3.Release(`${name}-release`, {
            name: releaseName,
            chart: args.chart ?? "oci://ghcr.io/rest-mail/charts/restmail",
            version: args.chartVersion,
            namespace: this.namespaceName,
            createNamespace: false,
            // Initial TLS setup can leave pods pending; server:verify remains
            // the readiness check, as with the existing Pulumi installer.
            skipAwait: true,
            values,
        }, { parent: this });
        this.url = pulumi.interpolate`https://${args.hostname}`;
        this.registerOutputs({
            namespace: this.namespaceName,
            releaseName: this.release.name,
            url: this.url,
        });
    }
}
