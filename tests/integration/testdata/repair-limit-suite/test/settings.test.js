const assert = require("node:assert/strict");
const test = require("node:test");

const settings = require("../settings.json");

test("every setting is a whole number", () => {
  for (const key of ["retries", "backoffSeconds", "timeoutSeconds"]) {
    assert.ok(Number.isInteger(settings[key]), `${key} is ${JSON.stringify(settings[key])}`);
  }
});

test("jobs retry at least 5 times", () => {
  assert.ok(settings.retries >= 5, `jobs retry ${settings.retries} times`);
});

test("retries wait between attempts", () => {
  assert.ok(settings.backoffSeconds >= 1, `the first retry waits ${settings.backoffSeconds} s`);
});

test("the timeout ends before the gateway's", () => {
  // The gateway closes a request after 30 s.
  assert.ok(settings.timeoutSeconds <= 30, `the timeout is ${settings.timeoutSeconds} s, past the gateway's 30 s`);
});

test("every retry fits within the timeout", () => {
  // The wait doubles after each retry, from backoffSeconds.
  const total = settings.backoffSeconds * (2 ** settings.retries - 1);
  assert.ok(total <= settings.timeoutSeconds,
    `${settings.retries} retries backing off from ${settings.backoffSeconds} s wait ${total} s, past the ${settings.timeoutSeconds} s timeout`);
});
