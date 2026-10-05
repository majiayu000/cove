const { test } = require("node:test");
const assert = require("node:assert/strict");
const fs = require("node:fs");
const vm = require("node:vm");
const { connect, restore, errorMessage } = require("./extension.cjs");

function fixture() {
  const profiles = new Map([["original", { id: "original-id", privateKey: "SYNTHETIC_ORIGINAL" }]]);
  let active = "original", saved;
  const roo = {
    getProfiles: () => [...profiles.keys()], getActiveProfile: () => active,
    getProfileEntry: (name) => profiles.has(name) ? { id: profiles.get(name).id } : undefined,
    createProfile: async (name, value) => { profiles.set(name, { ...value, id: "created-id" }); active = name; return "created-id"; },
    setActiveProfile: async (name) => { active = name; return name; },
    deleteProfile: async (name) => { profiles.delete(name); },
  };
  const state = { get: () => saved, update: async (_, value) => { saved = value; } };
  const input = { baseUrl: "https://cove.example.invalid/v1", model: "approved-model", apiKey: "SYNTHETIC_COVE_KEY" };
  return { roo, state, profiles, input };
}

test("native profile owns its Key; return and restore record contain no Key", async () => {
  const f = fixture(), result = await connect(f.roo, f.state, f.input);
  assert.equal(f.profiles.get("Cove").openAiApiKey, f.input.apiKey);
  assert.equal(f.roo.getActiveProfile(), "Cove");
  assert.ok(!JSON.stringify([result, f.state.get()]).includes(f.input.apiKey));
  await restore(f.roo, f.state, async () => true);
  assert.equal(f.roo.getActiveProfile(), "original");
  assert.equal(f.profiles.get("original").privateKey, "SYNTHETIC_ORIGINAL");
  assert.equal(f.profiles.has("Cove"), false);
  assert.equal(f.state.get(), undefined);
});
test("existing profile and a concurrent restore record cannot be overwritten", async () => {
  const f = fixture(); f.profiles.set("Cove", { id: "user-id" });
  await assert.rejects(connect(f.roo, f.state, f.input), /不会覆盖/);
  assert.equal(f.profiles.get("Cove").id, "user-id");
  f.profiles.delete("Cove"); await connect(f.roo, f.state, f.input);
  await assert.rejects(connect(f.roo, f.state, f.input), /不会覆盖/);
});
test("cancelled deletion preserves the profile and recovery record", async () => {
  const f = fixture(); await connect(f.roo, f.state, f.input);
  assert.deepEqual(await restore(f.roo, f.state, async () => false), { restored: false });
  assert.equal(f.roo.getActiveProfile(), "Cove"); assert.ok(f.state.get());
});
test("later user selection survives restoring the Cove profile", async () => {
  const f = fixture(); await connect(f.roo, f.state, f.input);
  f.profiles.set("later-user", { id: "later-id" }); await f.roo.setActiveProfile("later-user");
  await restore(f.roo, f.state, async () => true);
  assert.equal(f.roo.getActiveProfile(), "later-user");
});
test("recreated Cove or removed original profile is preserved", async () => {
  const f = fixture(); await connect(f.roo, f.state, f.input);
  f.profiles.set("Cove", { id: "replacement-id" });
  await assert.rejects(restore(f.roo, f.state, async () => true), /重新创建/);
  f.profiles.set("Cove", { id: "created-id" }); f.profiles.delete("original");
  await assert.rejects(restore(f.roo, f.state, async () => true), /原 profile 已移除/);
  assert.ok(f.profiles.has("Cove")); assert.ok(f.state.get());
});
test("a failed delete retains recovery state and is safely retried", async () => {
  const f = fixture(); await connect(f.roo, f.state, f.input);
  const remove = f.roo.deleteProfile; f.roo.deleteProfile = async () => { throw new Error("native delete failed"); };
  await assert.rejects(restore(f.roo, f.state, async () => true), /native delete failed/);
  assert.ok(f.state.get()); f.roo.deleteProfile = remove;
  await restore(f.roo, f.state, async () => true); assert.equal(f.state.get(), undefined);
});
test("failure to persist restoration data withdraws the just-created profile", async () => {
  const f = fixture(); f.state.update = async () => { throw new Error("state write failed"); };
  await assert.rejects(connect(f.roo, f.state, f.input), /新建 profile 已撤回/);
  assert.equal(f.roo.getActiveProfile(), "original"); assert.equal(f.profiles.has("Cove"), false);
});
test("credentials in endpoint URLs and errors are not exposed", async () => {
  const f = fixture();
  await assert.rejects(connect(f.roo, f.state, { ...f.input, baseUrl: "https://user:password@example.invalid/v1" }), /不含凭据/);
  f.roo.createProfile = async () => { throw new Error("native rejected " + f.input.apiKey); };
  const error = await connect(f.roo, f.state, f.input).then(() => null, e => e);
  assert.ok(!error.message.includes(f.input.apiKey));
  assert.equal(errorMessage(new Error("cove_123456789 sk-synthetic-secret")), "[redacted] [redacted]");
});
test("commands finish without waiting for success or error notifications", { timeout: 1000 }, async () => {
  const f = fixture(), commands = new Map(), never = new Promise(() => {});
  const vscode = {
    extensions: { getExtension: () => ({ packageJSON: { version: "3.53.0" }, activate: async () => f.roo }) },
    window: { showInformationMessage: () => never, showErrorMessage: () => never },
    commands: { registerCommand: (name, command) => { commands.set(name, command); return { dispose() {} }; } },
  };
  const sandbox = { module: { exports: {} }, require: (name) => { assert.equal(name, "vscode"); return vscode; }, URL };
  vm.runInNewContext(fs.readFileSync(require.resolve("./extension.cjs"), "utf8"), sandbox);
  sandbox.module.exports.activate({ subscriptions: [], globalState: f.state });
  await commands.get("cove.roo.connect")(f.input);
  assert.equal(f.roo.getActiveProfile(), "Cove");
  await assert.rejects(commands.get("cove.roo.connect")(f.input), /不会覆盖/);
});
