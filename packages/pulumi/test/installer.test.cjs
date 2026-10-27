const assert = require("node:assert/strict");
const { execFileSync } = require("node:child_process");
const path = require("node:path");
const { test } = require("node:test");
const ts = require("typescript");
const pulumi = require("@pulumi/pulumi");
const k8s = require("@pulumi/kubernetes");
const { RestMail } = require("../dist");

pulumi.runtime.setMocks({
    newResource(args) { return { id: `${args.name}-id`, state: args.inputs }; },
    call(args) { return args.inputs; },
}, "installer-tests", "test", true);

const script = path.resolve(__dirname, "../../../tasks/lifecycle/pulumi-code.sh");
const value = output => new Promise((resolve, reject) => {
    pulumi.output(output).apply(resolve).promise().catch(reject);
});

test("generated TypeScript executes with the component, encrypted config and explicit provider", async () => {
    const output = execFileSync("bash", [script], { encoding: "utf8", env: {
        ...process.env, RUNTIME: "nodejs", NAME: "mail-eu",
        PROJECT: "/a project with spaces", CHART: "oci://example.test/charts/restmail",
    } });
    assert.equal(output.includes("./vendor/rest-mail-pulumi-0.1.0.tgz"), true);
    assert.equal(output.includes("--pack-destination"), true);
    assert.equal(output.includes("new k8s.helm.v3.Release"), false);
    const source = output.slice(output.indexOf('import * as pulumi'), output.indexOf("\n3. Wire"));
    const compiled = ts.transpileModule(source, {
        compilerOptions: { module: ts.ModuleKind.CommonJS, target: ts.ScriptTarget.ES2022 },
        reportDiagnostics: true,
    });
    assert.deepEqual(compiled.diagnostics, []);
    const exports = {};
    const load = name => name === "@rest-mail/pulumi" ? { RestMail } : require(name);
    new Function("require", "exports", compiled.outputText)(load, exports);
    pulumi.runtime.setAllConfig({
        "mail-eu:hostname": "mail.example.test",
        "mail-eu:domain": "example.test",
        "mail-eu:namespace": "existing-mail",
        "mail-eu:createNamespace": "false",
        "mail-eu:chartVersion": "0.40.0",
        "mail-eu:adminPasswordHash": "fixture-hash",
        "mail-eu:ownerKeys": "fixture-public-key",
        "mail-eu:apnsPrivateKey": "synthetic-installer-key",
        "mail-eu:apnsKeyId": "KEY1234567",
        "mail-eu:apnsTeamId": "TEAM123456",
    }, ["mail-eu:apnsPrivateKey"]);
    const provider = new k8s.Provider("installed-cluster", {});
    const mail = exports.create_mail_eu(provider);
    assert.equal(mail instanceof RestMail, true);
    assert.equal(mail.namespace, undefined);
    assert.equal(await value(mail.release.chart), "oci://example.test/charts/restmail");
    assert.equal(await value(mail.release.namespace), "existing-mail");
    assert.equal(await pulumi.isSecret(mail.apnsSecret.stringData), true);
    assert.equal((await value(mail.release.values)).api.push.apns.keyId, "KEY1234567");
    assert.equal((await value(mail.release.values)).api.ownerKeys, "fixture-public-key");
});

test("Go, Python and YAML retain their direct-release installer path", () => {
    for (const runtime of ["go", "python", "yaml"]) {
        const output = execFileSync("bash", [script], { encoding: "utf8", env: {
            ...process.env, RUNTIME: runtime, NAME: "restmail", OWNER_KEYS_CONFIGURED: "1",
        } });
        assert.equal(output.includes("ownerKeys"), true);
        assert.equal(output.includes("@rest-mail/pulumi"), false);
    }
});
