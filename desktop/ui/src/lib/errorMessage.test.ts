import { describe, expect, it } from "vitest";
import { errorMessage } from "./errorMessage";

describe("errorMessage", () => {
  it("reads the message of a shell's rejection, a string", () => {
    expect(errorMessage("the daemon did not answer within 10 seconds; see logs/daemon.log in the HarmFlow folder")).toBe(
      "the daemon did not answer within 10 seconds; see logs/daemon.log in the HarmFlow folder",
    );
  });

  it("reads an Error's message", () => {
    expect(errorMessage(new Error("not allowed"))).toBe("not allowed");
  });
});
