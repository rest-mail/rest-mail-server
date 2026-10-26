const assert = require("node:assert/strict");
const { createHash } = require("node:crypto");
const { test } = require("node:test");
const pulumi = require("@pulumi/pulumi");
const k8s = require("@pulumi/kubernetes");

const { MockMonitor } = require("@pulumi/pulumi/runtime/mocks");
const registrations = [];
const registerResource = MockMonitor.prototype.registerResource;
MockMonitor.prototype.registerResource = function(request, callback) {
    registrations.push({
        name: request.getName(),
        retainOnDelete: request.getRetainondelete(),
        dependencies: request.getDependenciesList(),
        secretOutputs: request.getAdditionalsecretoutputsList(),
    });
    return registerResource.call(this, request, callback);
};
const resources = [];
pulumi.runtime.setMocks({
    newResource(args) {
        resources.push(args);
        return { id: `${args.name}-id`, state: args.inputs };
    },
    call(args) { return args.inputs; },
}, "component-tests", "test", true);

const { RestMail } = require("../dist");
const args = {
    hostname: "mail.example.test",
    domain: "example.test",
    chartVersion: "0.40.0",
    adminPasswordHash: "test-bootstrap-hash",
    ownerKeys: "test-public-owner-key",
};
const apns = { keyId: "KEY123", teamId: "TEAM123" };
const value = output => new Promise((resolve, reject) => {
    pulumi.output(output).apply(resolve).promise().catch(reject);
});

test("ordinary installation pins its chart and preserves extra chart settings", async () => {
    const mail = new RestMail("ordinary", {
        ...args,
        values: {
            postgresql: { enabled: false },
            api: { replicas: 2, push: { apns: { enabled: true, privateKey: "must-not-reach-helm" } } },
            mailserver: { hostname: "ignored.test", domain: "ignored.test", smtp: { enabled: true } },
        },
    });
    const values = await value(mail.release.values);
    assert.equal(values.api.replicas, 2);
    assert.deepEqual(values.api.push.apns, { enabled: false });
    assert.equal(values.mailserver.hostname, args.hostname);
    assert.equal(values.mailserver.domain, args.domain);
    assert.equal(values.mailserver.smtp.enabled, true);
    assert.equal(values.postgresql.enabled, false);
    assert.equal(values.api.bootstrapAdminPasswordHash, args.adminPasswordHash);
    assert.equal(values.api.ownerKeys, args.ownerKeys);
    assert.equal(await value(mail.release.version), args.chartVersion);
    assert.equal(await value(mail.release.chart), "oci://ghcr.io/rest-mail/charts/restmail");
    assert.equal(await value(mail.release.name), "ordinary");
    assert.equal(await value(mail.namespaceName), "ordinary");
    assert.equal(await value(mail.url), "https://mail.example.test");
    assert.equal(mail.apnsSecret, undefined);
    assert.equal(registrations.find(resource => resource.name === "ordinary-namespace").retainOnDelete, true);
    assert.equal(await value(mail.release.skipAwait), true);
});

test("managed APNs forces secret state and sends only a Secret reference to Helm", async () => {
    const fixture = "synthetic-test-credential-one";
    const mail = new RestMail("managed", {
        ...args, namespace: "mail-namespace", apns: { ...apns, privateKey: fixture },
    });
    const values = await value(mail.release.values);
    const secretData = await value(mail.apnsSecret.stringData);
    assert.equal(secretData["AuthKey.p8"], fixture);
    assert.equal(await pulumi.isSecret(mail.apnsSecret.stringData), true);
    const registration = registrations.find(resource => resource.name === "managed-apns");
    assert.equal(registration.secretOutputs.includes("data"), true);
    assert.equal(registration.secretOutputs.includes("stringData"), true);
    assert.equal(await pulumi.isSecret(mail.release.values), true);
    assert.equal(JSON.stringify(values).includes(fixture), false);
    assert.deepEqual(values.api.push.apns, {
        enabled: true,
        existingSecret: `managed-apns-${createHash("sha256").update(fixture).digest("hex").slice(0, 16)}`,
        secretKey: "AuthKey.p8", ...apns, topic: "com.antimatterstudios.restmail",
    });
    assert.equal((await value(mail.apnsSecret.metadata)).namespace, "mail-namespace");
    assert.equal(await value(mail.release.namespace), "mail-namespace");
    const release = registrations.find(resource => resource.name === "managed-release");
    assert.equal(release.dependencies.includes(await value(mail.apnsSecret.urn)), true);
    const secret = registrations.find(resource => resource.name === "managed-apns");
    assert.equal(secret.dependencies.includes(await value(mail.namespace.urn)), true);
});

test("encrypted config Outputs retain secret propagation and custom APNs topic", async () => {
    const mail = new RestMail("secret-output", {
        ...args, apns: { ...apns, privateKey: pulumi.secret("synthetic-test-credential-two"), topic: "test.bundle" },
    });
    assert.equal(await pulumi.isSecret(mail.apnsSecret.stringData), true);
    assert.equal((await value(mail.release.values)).api.push.apns.topic, "test.bundle");
});

test("key rotation changes the mounted Secret reference so the API rolls", async () => {
    const old = new RestMail("rotation-old", {
        ...args, releaseName: "rotating", createNamespace: false,
        apns: { ...apns, privateKey: "synthetic-old-key" },
    });
    const next = new RestMail("rotation-new", {
        ...args, releaseName: "rotating", createNamespace: false,
        apns: { ...apns, privateKey: "synthetic-new-key" },
    });
    const oldRef = (await value(old.release.values)).api.push.apns.existingSecret;
    const newRef = (await value(next.release.values)).api.push.apns.existingSecret;
    assert.notEqual(oldRef, newRef);
    assert.equal((await value(next.apnsSecret.metadata)).name, newRef);
});

test("existing namespaces and Secrets can remain owned by another stack", async () => {
    const mail = new RestMail("external", {
        ...args, namespace: "existing-mail", createNamespace: false,
        apns: { ...apns, existingSecret: pulumi.output("external-apns"), secretKey: "push.p8" },
    });
    const values = await value(mail.release.values);
    assert.equal(mail.namespace, undefined);
    assert.equal(mail.apnsSecret, undefined);
    assert.equal(values.api.push.apns.existingSecret, "external-apns");
    assert.equal(values.api.push.apns.secretKey, "push.p8");
    assert.equal(await value(mail.release.namespace), "existing-mail");
    assert.equal(await value(mail.release.createNamespace), false);
});

test("children use the caller's Kubernetes provider", async () => {
    const provider = new k8s.Provider("selected-cluster", {});
    const mail = new RestMail("provider-test", {
        ...args, apns: { ...apns, privateKey: "synthetic-provider-key" },
    }, { providers: { kubernetes: provider } });
    await Promise.all([value(mail.release.urn), value(mail.apnsSecret.urn), value(mail.namespace.urn)]);
    const urn = await value(provider.urn);
    for (const name of ["provider-test-namespace", "provider-test-apns", "provider-test-release"]) {
        assert.equal(resources.find(resource => resource.name === name).provider, `${urn}::selected-cluster-id`);
    }
});

test("ambiguous APNs sources and invalid Helm names fail without exposing keys", () => {
    assert.throws(() => new RestMail("ambiguous", {
        ...args, apns: { ...apns, privateKey: "synthetic-key", existingSecret: "existing" },
    }), /privateKey or existingSecret, never both/);
    assert.throws(() => new RestMail("Bad_Name", args), /lowercase Helm name/);
    assert.throws(() => new RestMail("long-name", { ...args, releaseName: "a".repeat(54) }), /at most 53/);
});
