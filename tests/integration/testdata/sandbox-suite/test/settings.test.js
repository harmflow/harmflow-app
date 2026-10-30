const assert = require("node:assert/strict");
const test = require("node:test");

const settings = require("../settings.json");

test("retries wait between attempts", () => {
  assert.ok(settings.retryDelaySeconds > 0, `the delay between retries is ${settings.retryDelaySeconds} s`);
});

test("every retry fits within the timeout", () => {
  const total = settings.retries * settings.retryDelaySeconds;
  assert.ok(total <= settings.timeoutSeconds,
    `${settings.retries} retries ${settings.retryDelaySeconds} s apart take ${total} s, past the ${settings.timeoutSeconds} s timeout`);
});
